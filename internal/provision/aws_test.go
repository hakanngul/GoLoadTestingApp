package provision

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

const testToken = "s3cr3t-w0rker-t0ken"

// fakeEC2 is an in-memory ec2API. capacity decides, per attempt key
// ("spot|c7i.large|subnet-a"), how many instances a RunInstances call can
// start; errs makes that attempt fail with the given EC2 error code.
type fakeEC2 struct {
	mu        sync.Mutex
	capacity  map[string]int
	errs      map[string]string
	calls     []*ec2.RunInstancesInput
	instances []types.Instance
	nextID    int
	terminate [][]string
	describe  []*ec2.DescribeInstancesInput
}

func newFake() *fakeEC2 {
	return &fakeEC2{capacity: map[string]int{}, errs: map[string]string{}}
}

func attemptKey(in *ec2.RunInstancesInput) string {
	market := "ondemand"
	if in.InstanceMarketOptions != nil && in.InstanceMarketOptions.MarketType == types.MarketTypeSpot {
		market = "spot"
	}
	return fmt.Sprintf("%s|%s|%s", market, in.InstanceType, aws.ToString(in.SubnetId))
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	key := attemptKey(in)
	if code, ok := f.errs[key]; ok {
		return nil, &smithy.GenericAPIError{Code: code, Message: "fake " + code}
	}
	n := min(f.capacity[key], int(aws.ToInt32(in.MaxCount)))
	if n < int(aws.ToInt32(in.MinCount)) {
		return nil, &smithy.GenericAPIError{Code: "InsufficientInstanceCapacity", Message: "fake: no capacity"}
	}
	var tags []types.Tag
	for _, ts := range in.TagSpecifications {
		if ts.ResourceType == types.ResourceTypeInstance {
			tags = ts.Tags
		}
	}
	out := &ec2.RunInstancesOutput{}
	for range n {
		f.nextID++
		inst := types.Instance{
			InstanceId:       aws.String(fmt.Sprintf("i-%04d", f.nextID)),
			PrivateIpAddress: aws.String(fmt.Sprintf("10.0.0.%d", f.nextID)),
			InstanceType:     in.InstanceType,
			State:            &types.InstanceState{Name: types.InstanceStateNamePending},
			Tags:             tags,
			LaunchTime:       aws.Time(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
		}
		if strings.HasPrefix(key, "spot") {
			inst.InstanceLifecycle = types.InstanceLifecycleTypeSpot
		}
		f.instances = append(f.instances, inst)
		out.Instances = append(out.Instances, inst)
	}
	return out, nil
}

func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminate = append(f.terminate, in.InstanceIds)
	for i := range f.instances {
		if slices.Contains(in.InstanceIds, aws.ToString(f.instances[i].InstanceId)) {
			f.instances[i].State = &types.InstanceState{Name: types.InstanceStateNameShuttingDown}
		}
	}
	return &ec2.TerminateInstancesOutput{}, nil
}

// DescribeInstances supports tag:<key> and instance-state-name filters.
func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describe = append(f.describe, in)
	var res types.Reservation
	for _, inst := range f.instances {
		if matches(inst, in.Filters) {
			res.Instances = append(res.Instances, inst)
		}
	}
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{res}}, nil
}

func matches(inst types.Instance, filters []types.Filter) bool {
	for _, flt := range filters {
		name := aws.ToString(flt.Name)
		var got string
		switch {
		case name == "instance-state-name":
			got = string(inst.State.Name)
		case strings.HasPrefix(name, "tag:"):
			for _, t := range inst.Tags {
				if aws.ToString(t.Key) == strings.TrimPrefix(name, "tag:") {
					got = aws.ToString(t.Value)
				}
			}
		default:
			panic("unsupported filter " + name)
		}
		if !slices.Contains(flt.Values, got) {
			return false
		}
	}
	return true
}

func (f *fakeEC2) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ks []string
	for _, c := range f.calls {
		ks = append(ks, attemptKey(c))
	}
	return ks
}

func testConfig() AWSConfig {
	return AWSConfig{
		Region:           "eu-central-1",
		LaunchTemplateID: "lt-0123456789abcdef0",
		SubnetIDs:        []string{"subnet-a", "subnet-b"},
		InstanceTypes:    []string{"c7i.large", "c6i.large", "c5.large"},
		WorkerImage:      "ghcr.io/example/goloadtest:v1.0.0",
		Tags:             map[string]string{"team": "perf"},
	}
}

func testRequest(n int) LaunchRequest {
	return LaunchRequest{RunID: "run-42", Count: n, MasterAddr: "10.0.1.15:9090", WorkerToken: testToken}
}

func TestLaunchFallbackOrder(t *testing.T) {
	f := newFake()
	// Spot: nothing anywhere except c5.large in subnet-b. Errors vary.
	f.errs["spot|c7i.large|subnet-a"] = "InsufficientInstanceCapacity"
	f.errs["spot|c7i.large|subnet-b"] = "SpotMaxPriceTooLow"
	f.errs["spot|c6i.large|subnet-a"] = "Unsupported"
	f.capacity["spot|c5.large|subnet-b"] = 10
	p := newAWS(testConfig(), f)

	got, err := p.Launch(context.Background(), testRequest(3))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("launched %d, want 3", len(got))
	}
	want := []string{
		"spot|c7i.large|subnet-a",
		"spot|c7i.large|subnet-b",
		"spot|c6i.large|subnet-a",
		"spot|c6i.large|subnet-b", // no capacity -> InsufficientInstanceCapacity
		"spot|c5.large|subnet-a",  // no capacity
		"spot|c5.large|subnet-b",  // success
	}
	if k := f.keys(); !slices.Equal(k, want) {
		t.Fatalf("attempt order\n got %v\nwant %v", k, want)
	}
	for _, i := range got {
		if !i.Spot || i.InstanceType != "c5.large" {
			t.Errorf("instance %+v: want spot c5.large", i)
		}
	}
}

func TestLaunchFallsBackToOnDemand(t *testing.T) {
	f := newFake()
	f.capacity["spot|c7i.large|subnet-a"] = 1
	f.errs["spot|c7i.large|subnet-b"] = "MaxSpotInstanceCountExceeded" // quota: skip all spot
	f.capacity["ondemand|c7i.large|subnet-a"] = 10
	p := newAWS(testConfig(), f)

	got, err := p.Launch(context.Background(), testRequest(4))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	want := []string{"spot|c7i.large|subnet-a", "spot|c7i.large|subnet-b", "ondemand|c7i.large|subnet-a"}
	if k := f.keys(); !slices.Equal(k, want) {
		t.Fatalf("attempt order\n got %v\nwant %v", k, want)
	}
	var spot int
	for _, i := range got {
		if i.Spot {
			spot++
		}
	}
	if len(got) != 4 || spot != 1 {
		t.Fatalf("got %d instances (%d spot), want 4 (1 spot)", len(got), spot)
	}
	// MaxCount must shrink to the remaining count.
	if mc := aws.ToInt32(f.calls[2].MaxCount); mc != 3 {
		t.Errorf("on-demand MaxCount = %d, want 3", mc)
	}
	if f.calls[2].InstanceMarketOptions != nil {
		t.Error("on-demand attempt must not set InstanceMarketOptions")
	}
}

func TestLaunchSpotOnlyPartialCapacity(t *testing.T) {
	f := newFake()
	f.capacity["spot|c7i.large|subnet-a"] = 2
	f.capacity["spot|c5.large|subnet-b"] = 1
	f.capacity["ondemand|c7i.large|subnet-a"] = 100 // must not be used
	cfg := testConfig()
	cfg.SpotOnly = true
	p := newAWS(cfg, f)

	got, err := p.Launch(context.Background(), testRequest(5))
	if len(got) != 3 {
		t.Fatalf("launched %d, want 3", len(got))
	}
	var sf *ShortfallError
	if !errors.As(err, &sf) {
		t.Fatalf("want *ShortfallError, got %v", err)
	}
	if sf.Requested != 5 || sf.Launched != 3 {
		t.Errorf("shortfall = %+v", sf)
	}
	for _, k := range f.keys() {
		if strings.HasPrefix(k, "ondemand") {
			t.Fatalf("SpotOnly attempted on-demand: %v", f.keys())
		}
	}
	// Every attempt error is reachable through the shortfall error.
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "InsufficientInstanceCapacity" {
		t.Errorf("want wrapped InsufficientInstanceCapacity, got %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("error message contains the worker token")
	}
}

func TestLaunchStopsOnFatalError(t *testing.T) {
	f := newFake()
	f.capacity["spot|c7i.large|subnet-a"] = 1
	f.errs["spot|c7i.large|subnet-b"] = "UnauthorizedOperation"
	p := newAWS(testConfig(), f)

	got, err := p.Launch(context.Background(), testRequest(3))
	if len(got) != 1 {
		t.Fatalf("launched %d, want the 1 that succeeded before the fatal error", len(got))
	}
	if err == nil || !strings.Contains(err.Error(), "UnauthorizedOperation") {
		t.Fatalf("want UnauthorizedOperation shortfall, got %v", err)
	}
	if n := len(f.keys()); n != 2 {
		t.Errorf("made %d RunInstances calls, want 2 (stop after fatal)", n)
	}
}

func TestLaunchRequestShape(t *testing.T) {
	f := newFake()
	f.capacity["spot|c7i.large|subnet-a"] = 2
	cfg := testConfig()
	cfg.MaxPrice = "0.05"
	cfg.MaxLifetime = 90 * time.Minute
	p := newAWS(cfg, f)
	if _, err := p.Launch(context.Background(), testRequest(2)); err != nil {
		t.Fatal(err)
	}
	in := f.calls[0]
	if lt := in.LaunchTemplate; aws.ToString(lt.LaunchTemplateId) != cfg.LaunchTemplateID || aws.ToString(lt.Version) != "$Default" {
		t.Errorf("launch template = %+v", lt)
	}
	so := in.InstanceMarketOptions.SpotOptions
	if so.SpotInstanceType != types.SpotInstanceTypeOneTime ||
		so.InstanceInterruptionBehavior != types.InstanceInterruptionBehaviorTerminate ||
		aws.ToString(so.MaxPrice) != "0.05" {
		t.Errorf("spot options = %+v", so)
	}
	if in.InstanceInitiatedShutdownBehavior != types.ShutdownBehaviorTerminate {
		t.Error("shutdown behaviour must be terminate")
	}
	if ct := aws.ToString(in.ClientToken); ct == "" || len(ct) > 64 {
		t.Errorf("client token %q", ct)
	}
	tags := map[string]string{}
	for _, ts := range in.TagSpecifications {
		for _, tg := range ts.Tags {
			tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
		}
	}
	if tags[TagRunID] != "run-42" || tags[TagRole] != RoleWorker || tags["team"] != "perf" {
		t.Errorf("tags = %v", tags)
	}

	raw, err := base64.StdEncoding.DecodeString(aws.ToString(in.UserData))
	if err != nil {
		t.Fatalf("user data is not base64: %v", err)
	}
	script := string(raw)
	for _, want := range []string{
		"#!/bin/bash",
		"GOLOAD_WORKER_TOKEN=" + testToken,
		"<<'ENV'", // quoted heredoc: no shell expansion of the token
		"worker -master 10.0.1.15:9090",
		"ghcr.io/example/goloadtest:v1.0.0",
		"net.ipv4.ip_local_port_range = 1024 65535",
		"net.ipv4.tcp_tw_reuse = 1",
		"fs.file-max",
		"ulimit -n 1048576",
		"shutdown -h +90",
		"--env-file /etc/goload/worker.env",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("user data missing %q", want)
		}
	}
	// The token must only appear in the env file, never on a command line.
	if n := strings.Count(script, testToken); n != 1 {
		t.Errorf("token appears %d times in user data, want 1", n)
	}
}

func TestUserDataECRLogin(t *testing.T) {
	params := userDataParams{RunID: "r", MasterAddr: "m:9090", Token: "t", LifetimeMinutes: 1}

	params.Image = "123456789012.dkr.ecr.eu-central-1.amazonaws.com/goloadtest:v1"
	script := decodeUserData(t, params)
	if !strings.Contains(script, "aws ecr get-login-password --region eu-central-1 | docker login --username AWS --password-stdin 123456789012.dkr.ecr.eu-central-1.amazonaws.com") {
		t.Errorf("ECR image without registry login:\n%s", script)
	}

	params.Image = "ghcr.io/example/goloadtest:v1"
	if script := decodeUserData(t, params); strings.Contains(script, "ecr get-login-password") {
		t.Error("non-ECR image must not trigger an ECR login")
	}
}

func decodeUserData(t *testing.T, p userDataParams) string {
	t.Helper()
	enc, err := renderUserData(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTokenNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	f := newFake()
	f.errs["spot|c7i.large|subnet-a"] = "InsufficientInstanceCapacity"
	f.capacity["spot|c7i.large|subnet-b"] = 1
	cfg := testConfig()
	cfg.SpotOnly = true
	cfg.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := newAWS(cfg, f)

	insts, _ := p.Launch(context.Background(), testRequest(2))
	_ = p.Terminate(context.Background(), []string{insts[0].ID})
	if buf.Len() == 0 {
		t.Fatal("expected log output")
	}
	if strings.Contains(buf.String(), testToken) {
		t.Fatalf("token leaked into logs:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "[REDACTED]") {
		t.Errorf("expected redacted request in logs:\n%s", buf.String())
	}
	req := testRequest(1)
	for _, s := range []string{fmt.Sprint(req), fmt.Sprintf("%v %+v %#v %s", req, req, req, req)} {
		if strings.Contains(s, testToken) {
			t.Errorf("formatted request leaks token: %s", s)
		}
	}
}

func TestListFiltersByRunTag(t *testing.T) {
	f := newFake()
	f.capacity["spot|c7i.large|subnet-a"] = 10
	p := newAWS(testConfig(), f)
	a, _ := p.Launch(context.Background(), LaunchRequest{RunID: "run-a", Count: 2, MasterAddr: "m:9090", WorkerToken: "t"})
	_, _ = p.Launch(context.Background(), LaunchRequest{RunID: "run-b", Count: 3, MasterAddr: "m:9090", WorkerToken: "t"})
	// An unrelated instance without goload tags.
	f.instances = append(f.instances, types.Instance{
		InstanceId: aws.String("i-other"),
		State:      &types.InstanceState{Name: types.InstanceStateNameRunning},
	})

	got, err := p.List(context.Background(), "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a[0].ID {
		t.Fatalf("List(run-a) = %+v", got)
	}
	all, _ := p.List(context.Background(), "")
	if len(all) != 5 {
		t.Errorf("List(\"\") = %d instances, want 5 goload workers", len(all))
	}

	// Terminated instances drop out of List.
	if err := p.Terminate(context.Background(), []string{a[0].ID}); err != nil {
		t.Fatal(err)
	}
	got, _ = p.List(context.Background(), "run-a")
	if len(got) != 1 || got[0].ID != a[1].ID {
		t.Fatalf("List after terminate = %+v", got)
	}

	// The request itself must filter on role and run tags.
	last := f.describe[len(f.describe)-1]
	names := map[string][]string{}
	for _, flt := range last.Filters {
		names[aws.ToString(flt.Name)] = flt.Values
	}
	if !slices.Equal(names["tag:goload:run-id"], []string{"run-a"}) || !slices.Equal(names["tag:goload:role"], []string{"worker"}) {
		t.Errorf("filters = %v", names)
	}
}

func TestTerminateBatchesAndEmpty(t *testing.T) {
	f := newFake()
	p := newAWS(testConfig(), f)
	if err := p.Terminate(context.Background(), nil); err != nil || len(f.terminate) != 0 {
		t.Fatalf("empty terminate: err=%v calls=%d", err, len(f.terminate))
	}
	ids := make([]string, 1200)
	for i := range ids {
		ids[i] = fmt.Sprintf("i-%d", i)
	}
	if err := p.Terminate(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(f.terminate) != 3 || len(f.terminate[2]) != 200 {
		t.Errorf("batches = %d (last %d)", len(f.terminate), len(f.terminate[len(f.terminate)-1]))
	}
}

func TestTerminateStale(t *testing.T) {
	f := newFake()
	f.capacity["spot|c7i.large|subnet-a"] = 10
	p := newAWS(testConfig(), f)
	old, _ := p.Launch(context.Background(), testRequest(2)) // LaunchTime 12:00
	fresh := types.Instance{
		InstanceId: aws.String("i-fresh"),
		State:      &types.InstanceState{Name: types.InstanceStateNameRunning},
		Tags:       []types.Tag{{Key: aws.String(TagRole), Value: aws.String(RoleWorker)}},
		LaunchTime: aws.Time(time.Date(2026, 1, 1, 15, 30, 0, 0, time.UTC)),
	}
	f.instances = append(f.instances, fresh)
	p.now = func() time.Time { return time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC) }

	got, err := p.TerminateStale(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{old[0].ID, old[1].ID}) {
		t.Fatalf("stale = %v, want %v", got, []string{old[0].ID, old[1].ID})
	}
	if _, err := p.TerminateStale(context.Background(), 0); err == nil {
		t.Error("zero age must be rejected")
	}
}

func TestValidation(t *testing.T) {
	f := newFake()
	p := newAWS(testConfig(), f)
	bad := []LaunchRequest{
		{RunID: "", Count: 1, MasterAddr: "m:9090", WorkerToken: "t"},
		{RunID: "r", Count: 0, MasterAddr: "m:9090", WorkerToken: "t"},
		{RunID: "r", Count: 1, MasterAddr: "m", WorkerToken: "t"},
		{RunID: "r", Count: 1, MasterAddr: "m:9090", WorkerToken: ""},
		{RunID: "r; rm -rf /", Count: 1, MasterAddr: "m:9090", WorkerToken: "t"},
		{RunID: "r", Count: 1, MasterAddr: "$(id):9090", WorkerToken: "t"},
		{RunID: "r", Count: 1, MasterAddr: "m:9090", WorkerToken: "tok\nENV\nrm -rf /"},
		{RunID: "r", Count: 1, MasterAddr: "m:9090", WorkerToken: "has space"},
	}
	for _, req := range bad {
		if _, err := p.Launch(context.Background(), req); err == nil {
			t.Errorf("Launch(%v) succeeded, want validation error", req)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("invalid requests reached EC2 (%d calls)", len(f.calls))
	}

	cfgs := []AWSConfig{
		{WorkerImage: "img"},
		{LaunchTemplateID: "lt-1"},
		{LaunchTemplateID: "lt-1", WorkerImage: "img; reboot"},
		{LaunchTemplateID: "lt-1", WorkerImage: "img", Tags: map[string]string{"aws:x": "y"}},
		{LaunchTemplateID: "lt-1", WorkerImage: "img", Tags: map[string]string{TagRunID: "y"}},
	}
	for _, c := range cfgs {
		if err := c.validate(); err == nil {
			t.Errorf("config %+v accepted", c)
		}
	}
}

func TestNoop(t *testing.T) {
	var p Provisioner = Noop{}
	if _, err := p.Launch(context.Background(), testRequest(1)); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Launch err = %v", err)
	}
	if err := p.Terminate(context.Background(), []string{"x"}); err != nil {
		t.Error(err)
	}
	if l, err := p.List(context.Background(), "r"); err != nil || l != nil {
		t.Error(l, err)
	}
}
