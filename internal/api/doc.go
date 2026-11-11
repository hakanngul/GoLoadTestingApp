// Package api serves the REST API, the SSE live stream and the embedded UI.
//
// CONTRACT (routes). JSON bodies use internal/model types. Errors are
// model.APIError with an appropriate status (400 validation, 404, 409, 500).
//
//	GET    /api/health                       -> {"status":"ok","version":"...","retention_seconds":N}
//	                                            (serve -retention; 0 = runs are kept forever)
//
//	GET    /api/tests                        -> []model.Test (LastRun filled)
//	POST   /api/tests                        {name, yaml} -> 201 model.Test   (400 if yaml invalid: APIError.Details = validation errors)
//	GET    /api/tests/{id}                   -> model.Test
//	PUT    /api/tests/{id}                   {name, yaml} -> model.Test
//	DELETE /api/tests/{id}                   -> 204
//	PUT    /api/tests/{id}/files/{name}      raw body (CSV, max 50 MiB) -> 204
//	DELETE /api/tests/{id}/files/{name}      -> 204
//	POST   /api/validate                     model.ValidateRequest {yaml, test_id?} -> model.ValidateResult (always 200;
//	                                         warnings = {{var}} references no source defines; test_id lets the
//	                                         server read the uploaded data file's header for CSV columns)
//	POST   /api/tests/try                    model.TryRequest {yaml, test_id?} -> model.TryResult
//	                                         "Test request": runs ONE iteration (auth login + each step once, in
//	                                         order) through the same executors as runs and reports every step
//	                                         (rendered URL, redacted headers, status, duration, selected response
//	                                         headers, body snippet <= 8 KiB text, extracts, assertions) plus hints.
//	                                         Redirects are followed like in runs: a step's status/headers/body are
//	                                         those of the final response, `redirects` lists every followed hop
//	                                         {status, method, url, location, cookies_sent?, cookies_set?} and
//	                                         `final_url` the last request. `cookies_sent` / `cookies_set` name the
//	                                         cookies sent with / stored from the final exchange (names only, never
//	                                         values). `location` is set only for a final 3xx (not followed).
//	                                         Stops at the first failing step (later steps: not_run). Uses the first
//	                                         row of the test's data file (test_id; unsaved tests run without data,
//	                                         see data_note). Target allowlist enforced like runs (rejection is a
//	                                         hint, nothing is sent), also for every redirect hop to another host. Limits: 30 s total, scenario timeout per step,
//	                                         first 20 steps, think times / ws hold skipped, 4 concurrent tries
//	                                         server-wide (429 + Retry-After otherwise). 400 for invalid yaml,
//	                                         404 for an unknown test_id.
//
//	POST   /api/tests/{id}/runs              model.StartRunRequest -> 201 model.Run
//	GET    /api/runs?test_id=&limit=         -> []model.Run (newest first)
//	GET    /api/runs/{id}                    -> model.Run
//	POST   /api/runs/{id}/stop               -> 202
//	GET    /api/runs/{id}/metrics            -> []model.MetricPoint
//	GET    /api/runs/{id}/errors             -> []model.ErrorSample
//	GET    /api/runs/{id}/error-groups       -> []model.ErrorGroup (final counts; live counts while running)
//	GET    /api/runs/{id}/stream             SSE: `event: metric|status|done|stats`, `data: <json>`.
//	                                         stats = model.LiveStats (cumulative per-step table + error groups), 1/s.
//	                                         For a finished run: one `done` event then close.
//	                                         Heartbeat comment `: ping` every 15s.
//
//	GET    /api/workers                      -> []model.Worker
//
//	Reporting (window = full|steady|custom; custom needs from=&to= seconds;
//	default steady; tolerance default 0.10):
//	PATCH  /api/runs/{id}                    model.UpdateRunRequest -> model.Run
//	DELETE /api/runs/{id}                    -> 204 (409 while active)
//	GET    /api/runs/{id}/report?window=&from=&to=  -> model.RunReport
//	GET    /api/runs/{id}/report.html?window=...    self-contained printable HTML report (download)
//	GET    /api/runs/{id}/export.csv         per-second metric points (CSV)
//	GET    /api/runs/{id}/export.json        {run, metrics, errors} (JSON download)
//	GET    /api/compare?runs=a,b[,..6]&window=&tolerance=  -> model.CompareResult
//	GET    /api/tests/{id}/trend?limit=30&window=   -> []model.TrendPoint (oldest first, finished runs)
//	PUT    /api/tests/{id}/baseline          {"run_id": "..."} (or "" to clear) -> model.Test
//	GET    /api/reports/overview?days=30     -> model.Overview
//
//	Schedules (internal/scheduler; 503 when the server has no scheduler):
//	GET    /api/schedules?test_id=           -> []model.Schedule (oldest first; test_name / suite_name filled)
//	POST   /api/schedules                    model.ScheduleRequest -> 201 model.Schedule
//	                                         (a schedule targets exactly one of test_id and suite_id; a
//	                                         suite schedule starts a suite run per occurrence and must not
//	                                         set load or workers; last_run_id is then a suite run ID)
//	                                         (400 "invalid schedule", Details = "field: message"; enabled
//	                                         defaults to true; next_run_at computed from the definition)
//	GET    /api/schedules/{id}               -> model.Schedule
//	PUT    /api/schedules/{id}               model.ScheduleRequest -> model.Schedule (next run recomputed
//	                                         from now; last_* kept)
//	DELETE /api/schedules/{id}               -> 204 (runs it started keep schedule_id)
//	POST   /api/schedules/{id}/run-now       -> 201 model.Run (starts now with the schedule's overrides,
//	                                         ignoring timing, enabled and overlap; next_run_at unchanged);
//	                                         for a suite schedule 201 {"suite_run": model.SuiteRun}
//	                                         (409 when the suite is already running)
//	POST   /api/schedules/preview            model.SchedulePreviewRequest {kind, at|cron, timezone, count}
//	                                         -> model.SchedulePreview (always 200; next count fire times,
//	                                         default 5, max 50, or errors)
//
//	GET    /api/suites                       -> []model.Suite (name order; item test_name filled, "" =
//	                                         deleted test; last_run filled)
//	POST   /api/suites                       model.SuiteRequest -> 201 model.Suite (400 invalid suite,
//	                                         409 duplicate name)
//	GET    /api/suites/{id}                  -> model.Suite
//	PUT    /api/suites/{id}                  model.SuiteRequest -> model.Suite
//	DELETE /api/suites/{id}                  -> 204 (deletes its suite runs; 409 while running)
//	POST   /api/suites/{id}/runs             model.StartSuiteRequest (optional) -> 201 model.SuiteRun
//	                                         (409 already running)
//	GET    /api/suites/{id}/runs?limit=      -> []model.SuiteRun (newest first)
//	GET    /api/suite-runs?suite_id=&limit=  -> []model.SuiteRun (newest first)
//	GET    /api/suite-runs/{id}              -> model.SuiteRun (poll while running)
//	POST   /api/suite-runs/{id}/stop         -> 202 (409 not active)
//
//	Notifications (channels live in the store's store.NotificationStore; 503 when the store does not
//	implement it). A channel URL is a secret: responses carry only "url_masked"
//	(scheme://host/…last4) and never "url"; URLs are never logged:
//	GET    /api/notifications                -> []model.NotificationView (oldest first)
//	POST   /api/notifications                model.NotificationRequest -> 201 model.NotificationView
//	                                         (400 "invalid notification channel", Details = "field: message":
//	                                         name required <=100, kind slack|webhook, url required and https
//	                                         (http only for localhost/127.0.0.1/::1), events non-empty subset of
//	                                         run_finished|suite_finished; enabled defaults to true)
//	PUT    /api/notifications/{id}           model.NotificationRequest -> model.NotificationView (empty url
//	                                         keeps the stored one; enabled omitted keeps the stored value)
//	DELETE /api/notifications/{id}           -> 204
//	POST   /api/notifications/{id}/test      -> 200 {"ok": true} or {"ok": false, "error": "..."} after a
//	                                         synchronous test message (503 without a Notifier)
//
//	GET    /*                              embedded UI (web/dist), SPA fallback to index.html
//
// Scenario validation for POST/PUT/validate uses scenario.Parse; the data
// file referenced by `data.file` need not be uploaded yet at save time,
// but must exist when a run starts.
package api
