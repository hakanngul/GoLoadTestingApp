// Package api serves the REST API, the SSE live stream and the embedded UI.
//
// CONTRACT (routes). JSON bodies use internal/model types. Errors are
// model.APIError with an appropriate status (400 validation, 404, 409, 500).
//
//	GET    /api/health                       -> {"status":"ok","version":"..."}
//
//	GET    /api/tests                        -> []model.Test (LastRun filled)
//	POST   /api/tests                        {name, yaml} -> 201 model.Test   (400 if yaml invalid: APIError.Details = validation errors)
//	GET    /api/tests/{id}                   -> model.Test
//	PUT    /api/tests/{id}                   {name, yaml} -> model.Test
//	DELETE /api/tests/{id}                   -> 204
//	PUT    /api/tests/{id}/files/{name}      raw body (CSV, max 50 MiB) -> 204
//	DELETE /api/tests/{id}/files/{name}      -> 204
//	POST   /api/validate                     {yaml} -> model.ValidateResult (always 200)
//
//	POST   /api/tests/{id}/runs              model.StartRunRequest -> 201 model.Run
//	GET    /api/runs?test_id=&limit=         -> []model.Run (newest first)
//	GET    /api/runs/{id}                    -> model.Run
//	POST   /api/runs/{id}/stop               -> 202
//	GET    /api/runs/{id}/metrics            -> []model.MetricPoint
//	GET    /api/runs/{id}/errors             -> []model.ErrorSample
//	GET    /api/runs/{id}/stream             SSE: `event: metric|status|done`, `data: <json>`.
//	                                         For a finished run: one `done` event then close.
//	                                         Heartbeat comment `: ping` every 15s.
//
//	GET    /api/workers                      -> []model.Worker
//
//	GET    /*                                embedded UI (web/dist), SPA fallback to index.html
//
// Scenario validation for POST/PUT/validate uses scenario.Parse; the data
// file referenced by `data.file` need not be uploaded yet at save time,
// but must exist when a run starts.
package api
