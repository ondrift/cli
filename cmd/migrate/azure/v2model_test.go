package azure

import (
	"strings"
	"testing"
)

// TestParseV2Functions covers the v2 in-code model: app-level auth, http routes
// (incl. methods + route params), timer→cron, a multi-line queue_trigger, a
// default-auth route, and an unsupported trigger that must refuse.
func TestParseV2Functions(t *testing.T) {
	src := []byte(`import azure.functions as func

app = func.FunctionApp(http_auth_level=func.AuthLevel.ANONYMOUS)


@app.route(route="orders/{id}", methods=["GET"])
def get_order(req: func.HttpRequest) -> func.HttpResponse:
    return func.HttpResponse("ok")


@app.schedule(schedule="0 0 2 * * *", arg_name="timer", run_on_startup=False)
def nightly(timer: func.TimerRequest) -> None:
    pass


@app.queue_trigger(arg_name="msg", queue_name="notify",
                   connection="AzureWebJobsStorage")
def on_message(msg: func.QueueMessage) -> None:
    pass


@app.blob_trigger(arg_name="blob", path="uploads/{name}", connection="st")
def on_blob(blob: func.InputStream) -> None:
    pass


@app.route(route="health")
def health(req):
    return func.HttpResponse("healthy")
`)
	fns, refs := parseV2Functions("orders-api", src, "atomic/orders-api-v2/function_app.py")

	byName := map[string]ManifestFunction{}
	for _, f := range fns {
		byName[f.Name] = f
	}

	if got := byName["get_order"].Trigger; got.Type != "http" || got.Method != "get" || got.Route != "orders/:id" || got.Auth != "none" {
		t.Errorf("get_order trigger = %+v, want http get orders/:id auth=none", got)
	}
	if got := byName["nightly"].Trigger; got.Type != "cron" || got.Schedule != "0 2 * * *" {
		t.Errorf("nightly trigger = %+v, want cron 0 2 * * *", got)
	}
	if got := byName["on_message"].Trigger; got.Type != "queue" || got.Queue != "notify" {
		t.Errorf("on_message (multi-line decorator) trigger = %+v, want queue notify", got)
	}
	if got := byName["health"].Trigger; got.Type != "http" || got.Route != "health" || got.Auth != "none" {
		t.Errorf("health trigger = %+v, want http route=health auth=none (app-level)", got)
	}
	if _, ok := byName["on_blob"]; ok {
		t.Error("on_blob (blob_trigger) must be refused, not parsed as a function")
	}
	var blobRefused bool
	for _, r := range refs {
		if strings.Contains(r.Resource, "on_blob") {
			blobRefused = true
		}
	}
	if !blobRefused {
		t.Errorf("expected on_blob refused; got refusals %+v", refs)
	}

	if !isV2Model(map[string][]byte{"function_app.py": src, "host.json": {}}) {
		t.Error("isV2Model should detect function_app.py with no function.json")
	}
	if isV2Model(map[string][]byte{"function_app.py": src, "GetOrder/function.json": {}}) {
		t.Error("isV2Model should be false when v1 function.json folders are present")
	}
}
