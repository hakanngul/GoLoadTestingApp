package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// Tag keys written on every worker instance (and its volumes).
const (
	TagRunID     = "goload:run-id"
	TagRole      = "goload:role"
	RoleWorker   = "worker"
	tagName      = "Name"
	terminateMax = 500 // ids per TerminateInstances call
)

// Defaults applied by NewAWS when the corresponding AWSConfig field is empty.
const (
	DefaultLaunchTemplateVersion = "$Default"
	DefaultWorkerLifetime        = 6 * time.Hour
)

// ec2API is the subset of the EC2 client used by AWS; it exists so tests can
// substitute a fake. *ec2.Client satisfies it.
type ec2API interface {
	RunInstances(ctx context.Context, in *ec2.RunInstancesInput, opts ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	TerminateInstances(ctx context.Context, in *ec2.TerminateInstancesInput, opts ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

// AWSConfig configures the EC2 provisioner. Network, AMI, security group and
// instance profile come from the launch template (see deploy/aws).
type AWSConfig struct {
	// Region, e.g. "eu-central-1". Empty uses the SDK default chain
	// (AWS_REGION, shared config, IMDS).
	Region string
	// LaunchTemplateID of the worker launch template (required).
	LaunchTemplateID string
	// LaunchTemplateVersion, default "$Default".
	LaunchTemplateVersion string
	// SubnetIDs to try in order (one per AZ spreads capacity risk). Empty
	// uses the subnet from the launch template / default VPC.
	SubnetIDs []string
	// InstanceTypes is the fallback list tried in order, e.g.
	// {"c7i.large", "c6i.large", "c5.large"}. Empty uses the template's type.
	InstanceTypes []string
	// MaxPrice is the optional spot price cap in USD/hour ("0.05"). Empty
	// means the on-demand price, which is AWS's recommended setting.
	MaxPrice string
	// Tags are extra tags put on every worker instance and volume.
	Tags map[string]string
	// SpotOnly disables the on-demand fallback when no spot capacity is
	// available in any type/subnet combination.
	SpotOnly bool
	// WorkerImage is the container image run by workers (required), e.g.
	// "123456789012.dkr.ecr.eu-central-1.amazonaws.com/goloadtest:v1.2.3".
	WorkerImage string
	// MaxLifetime makes each worker shut itself down (and thereby terminate)
	// after this long, even if the master never calls Terminate. Default 6h.
	MaxLifetime time.Duration
	// Logger receives launch/terminate events. Secrets are never logged.
	// Nil discards logs.
	Logger *slog.Logger
}

// AWS provisions workers as EC2 instances from a launch template.
type AWS struct {
	cfg AWSConfig
	api ec2API
	log *slog.Logger
	now func() time.Time
}

var _ Provisioner = (*AWS)(nil)

// NewAWS validates cfg and creates an EC2-backed provisioner using the
// default AWS credential chain (env, shared config, instance role).
func NewAWS(ctx context.Context, cfg AWSConfig) (*AWS, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	var opts []func(*config.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, config.WithRegion(cfg.Region))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("provision: load AWS config: %w", err)
	}
	return newAWS(cfg, ec2.NewFromConfig(awsCfg)), nil
}

// newAWS wires a provisioner to an arbitrary EC2 API (used by tests).
// cfg must already be valid.
func newAWS(cfg AWSConfig, api ec2API) *AWS {
	if cfg.LaunchTemplateVersion == "" {
		cfg.LaunchTemplateVersion = DefaultLaunchTemplateVersion
	}
	if cfg.MaxLifetime <= 0 {
		cfg.MaxLifetime = DefaultWorkerLifetime
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AWS{cfg: cfg, api: api, log: log, now: time.Now}
}

func (c AWSConfig) validate() error {
	if c.LaunchTemplateID == "" {
		return errors.New("provision: AWS LaunchTemplateID is required")
	}
	if c.WorkerImage == "" {
		return errors.New("provision: AWS WorkerImage is required")
	}
	if !safeImage.MatchString(c.WorkerImage) {
		return fmt.Errorf("provision: worker image %q is not a valid image reference", c.WorkerImage)
	}
	for k := range c.Tags {
		if strings.HasPrefix(strings.ToLower(k), "aws:") {
			return fmt.Errorf("provision: tag key %q uses the reserved aws: prefix", k)
		}
		if k == TagRunID || k == TagRole {
			return fmt.Errorf("provision: tag key %q is managed by goload", k)
		}
	}
	return nil
}

// launchAttempt is one (market, instance type, subnet) combination.
type launchAttempt struct {
	spot         bool
	instanceType string // "" = launch template's type
	subnetID     string // "" = launch template's / default subnet
}

func (a launchAttempt) String() string {
	market := "on-demand"
	if a.spot {
		market = "spot"
	}
	return fmt.Sprintf("%s/%s/%s", market, orDefault(a.instanceType), orDefault(a.subnetID))
}

func orDefault(s string) string {
	if s == "" {
		return "template-default"
	}
	return s
}

// attempts returns the launch plan: every instance type (in order) across
// every subnet (in order) as spot, then the same as on-demand unless SpotOnly.
func (a *AWS) attempts() []launchAttempt {
	itypes := a.cfg.InstanceTypes
	if len(itypes) == 0 {
		itypes = []string{""}
	}
	subnets := a.cfg.SubnetIDs
	if len(subnets) == 0 {
		subnets = []string{""}
	}
	markets := []bool{true}
	if !a.cfg.SpotOnly {
		markets = append(markets, false)
	}
	var out []launchAttempt
	for _, spot := range markets {
		for _, it := range itypes {
			for _, sn := range subnets {
				out = append(out, launchAttempt{spot: spot, instanceType: it, subnetID: sn})
			}
		}
	}
	return out
}

// failureScope says how far a launch error reaches.
type failureScope int

const (
	scopeFatal   failureScope = iota // stop launching altogether
	scopeAttempt                     // try the next type/subnet
	scopeMarket                      // skip the rest of this market (spot -> on-demand)
)

// classify maps an EC2 error code to how Launch should react.
func classify(err error) failureScope {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return scopeFatal
	}
	switch apiErr.ErrorCode() {
	case "InsufficientInstanceCapacity", // no capacity for this type in this AZ
		"SpotMaxPriceTooLow",                // spot price above MaxPrice for this type/AZ
		"Unsupported",                       // type not offered in this AZ
		"InsufficientFreeAddressesInSubnet": // subnet full
		return scopeAttempt
	case "MaxSpotInstanceCountExceeded", // account spot vCPU quota
		"VcpuLimitExceeded",     // account on-demand vCPU quota
		"InstanceLimitExceeded": // legacy instance-count limit
		return scopeMarket
	}
	return scopeFatal
}

// Launch starts req.Count workers, walking the fallback plan until enough
// instances run. Instances that were started are always returned, together
// with a *ShortfallError when fewer than req.Count could be launched.
func (a *AWS) Launch(ctx context.Context, req LaunchRequest) ([]Instance, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	userData, err := renderUserData(userDataParams{
		RunID:           req.RunID,
		MasterAddr:      req.MasterAddr,
		Token:           req.WorkerToken,
		Image:           a.cfg.WorkerImage,
		LifetimeMinutes: lifetimeMinutes(a.cfg.MaxLifetime),
	})
	if err != nil {
		return nil, err
	}
	a.log.Info("launching workers", "request", req, "plan", len(a.attempts()))

	var (
		launched     []Instance
		failures     []error
		skipSpot     bool
		skipOnDemand bool
	)
	for _, at := range a.attempts() {
		remaining := req.Count - len(launched)
		if remaining <= 0 {
			break
		}
		if (at.spot && skipSpot) || (!at.spot && skipOnDemand) {
			continue
		}
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		got, err := a.runInstances(ctx, req.RunID, at, remaining, userData)
		launched = append(launched, got...)
		if err == nil {
			a.log.Info("workers launched", "run_id", req.RunID, "attempt", at.String(), "count", len(got))
			continue
		}
		err = fmt.Errorf("%s: %w", at, err)
		failures = append(failures, err)
		scope := classify(err)
		a.log.Warn("worker launch attempt failed", "run_id", req.RunID, "attempt", at.String(), "error", err)
		if scope == scopeFatal {
			break
		}
		if scope == scopeMarket {
			if at.spot {
				skipSpot = true
			} else {
				skipOnDemand = true
			}
		}
	}

	if len(launched) < req.Count {
		return launched, &ShortfallError{Requested: req.Count, Launched: len(launched), Attempts: failures}
	}
	return launched, nil
}

// runInstances performs a single RunInstances call for up to count workers.
// MinCount is 1 so EC2 launches as many as it can (partial capacity).
func (a *AWS) runInstances(ctx context.Context, runID string, at launchAttempt, count int, userData string) ([]Instance, error) {
	in := &ec2.RunInstancesInput{
		MinCount: aws.Int32(1),
		MaxCount: aws.Int32(int32(count)),
		LaunchTemplate: &types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(a.cfg.LaunchTemplateID),
			Version:          aws.String(a.cfg.LaunchTemplateVersion),
		},
		UserData:                          aws.String(userData),
		InstanceInitiatedShutdownBehavior: types.ShutdownBehaviorTerminate,
		TagSpecifications:                 a.tagSpecs(runID),
		ClientToken:                       aws.String(clientToken(runID, at, count, a.now())),
	}
	if at.instanceType != "" {
		in.InstanceType = types.InstanceType(at.instanceType)
	}
	if at.subnetID != "" {
		in.SubnetId = aws.String(at.subnetID)
	}
	if at.spot {
		spot := &types.SpotMarketOptions{
			SpotInstanceType:             types.SpotInstanceTypeOneTime,
			InstanceInterruptionBehavior: types.InstanceInterruptionBehaviorTerminate,
		}
		if a.cfg.MaxPrice != "" {
			spot.MaxPrice = aws.String(a.cfg.MaxPrice)
		}
		in.InstanceMarketOptions = &types.InstanceMarketOptionsRequest{
			MarketType:  types.MarketTypeSpot,
			SpotOptions: spot,
		}
	}
	out, err := a.api.RunInstances(ctx, in)
	if err != nil {
		return nil, err
	}
	insts := make([]Instance, 0, len(out.Instances))
	for _, i := range out.Instances {
		insts = append(insts, toInstance(i))
	}
	return insts, nil
}

// clientToken makes RunInstances idempotent if the SDK retries a call whose
// response was lost. EC2 allows at most 64 ASCII characters.
func clientToken(runID string, at launchAttempt, count int, now time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s", runID, now.UnixNano(), count, at)))
	return "goload-" + hex.EncodeToString(sum[:])[:48]
}

func (a *AWS) tagSpecs(runID string) []types.TagSpecification {
	tags := []types.Tag{
		{Key: aws.String(TagRunID), Value: aws.String(runID)},
		{Key: aws.String(TagRole), Value: aws.String(RoleWorker)},
		{Key: aws.String(tagName), Value: aws.String("goload-worker-" + runID)},
	}
	for k, v := range a.cfg.Tags {
		if k == tagName {
			tags[2].Value = aws.String(v)
			continue
		}
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return []types.TagSpecification{
		{ResourceType: types.ResourceTypeInstance, Tags: tags},
		{ResourceType: types.ResourceTypeVolume, Tags: tags},
	}
}

// Terminate terminates the given instances in batches.
func (a *AWS) Terminate(ctx context.Context, ids []string) error {
	var errs []error
	for start := 0; start < len(ids); start += terminateMax {
		batch := ids[start:min(start+terminateMax, len(ids))]
		_, err := a.api.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: batch})
		if err != nil {
			errs = append(errs, fmt.Errorf("provision: terminate %d instances: %w", len(batch), err))
			continue
		}
		a.log.Info("workers terminated", "count", len(batch), "ids", batch)
	}
	return errors.Join(errs...)
}

// liveStates are the instance states that still cost (or may soon cost) money.
var liveStates = []string{"pending", "running", "stopping", "stopped"}

// List returns the live workers of runID (all goload workers if runID is "").
func (a *AWS) List(ctx context.Context, runID string) ([]Instance, error) {
	return a.describe(ctx, runID)
}

func (a *AWS) describe(ctx context.Context, runID string) ([]Instance, error) {
	filters := []types.Filter{
		{Name: aws.String("tag:" + TagRole), Values: []string{RoleWorker}},
		{Name: aws.String("instance-state-name"), Values: liveStates},
	}
	if runID != "" {
		filters = append(filters, types.Filter{Name: aws.String("tag:" + TagRunID), Values: []string{runID}})
	}
	p := ec2.NewDescribeInstancesPaginator(a.api, &ec2.DescribeInstancesInput{Filters: filters})
	var out []Instance
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("provision: describe instances: %w", err)
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				out = append(out, toInstance(i))
			}
		}
	}
	return out, nil
}

// TerminateStale terminates every goload worker (of any run) launched more
// than olderThan ago and returns the IDs it terminated. Run it periodically
// from the master as a safety net against orphaned workers, e.g. after a
// master crash. olderThan should comfortably exceed the longest run.
func (a *AWS) TerminateStale(ctx context.Context, olderThan time.Duration) ([]string, error) {
	if olderThan <= 0 {
		return nil, errors.New("provision: TerminateStale needs a positive age")
	}
	insts, err := a.describe(ctx, "")
	if err != nil {
		return nil, err
	}
	cutoff := a.now().Add(-olderThan)
	var stale []string
	for _, i := range insts {
		if !i.LaunchedAt.IsZero() && i.LaunchedAt.Before(cutoff) {
			stale = append(stale, i.ID)
		}
	}
	if len(stale) == 0 {
		return nil, nil
	}
	a.log.Warn("terminating stale workers", "count", len(stale), "older_than", olderThan.String())
	if err := a.Terminate(ctx, stale); err != nil {
		return nil, err
	}
	return stale, nil
}

func toInstance(i types.Instance) Instance {
	inst := Instance{
		ID:           aws.ToString(i.InstanceId),
		PrivateIP:    aws.ToString(i.PrivateIpAddress),
		Spot:         i.InstanceLifecycle == types.InstanceLifecycleTypeSpot,
		InstanceType: string(i.InstanceType),
		LaunchedAt:   aws.ToTime(i.LaunchTime),
	}
	if i.State != nil {
		inst.State = string(i.State.Name)
	}
	return inst
}
