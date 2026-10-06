package openapi

import (
	"encoding/json"
	"testing"
)

const fixtureOpenAPI = `{
  "openapi": "3.0.0",
  "info": {"title": "Orders API", "version": "1.0"},
  "paths": {
    "/orders/{id}": {
      "get": {
        "operationId": "getOrder",
        "parameters": [
          {"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}
        ],
        "responses": {
          "200": {
            "description": "OK",
            "content": {
              "application/json": {
                "example": {"id": "abc", "status": "pending"}
              }
            }
          }
        }
      }
    },
    "/orders": {
      "post": {
        "operationId": "createOrder",
        "requestBody": {
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["customerName"],
                "properties": {
                  "customerName": {"type": "string"}
                }
              }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Created",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "id": {"type": "string"},
                    "status": {"type": "string"}
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`

func parseFixtureOps(t *testing.T) map[string]ScaffoldOperation {
	t.Helper()
	ops, err := Parse([]byte(fixtureOpenAPI))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 operations, got %d: %+v", len(ops), ops)
	}
	byName := map[string]ScaffoldOperation{}
	for _, op := range ops {
		byName[op.Name] = op
	}
	return byName
}

func TestParseFixtureOpenAPIScaffoldsGetOrder(t *testing.T) {
	byName := parseFixtureOps(t)
	getOrder, ok := byName["getOrder"]
	if !ok {
		t.Fatalf("expected a getOrder operation, got %+v", byName)
	}
	if getOrder.Method != "GET" || getOrder.Path != "/orders/{id}" {
		t.Fatalf("unexpected getOrder method/path: %+v", getOrder)
	}
	if getOrder.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", getOrder.StatusCode)
	}
	if getOrder.ContentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", getOrder.ContentType)
	}
	var example map[string]any
	if err := json.Unmarshal([]byte(getOrder.ExampleJSON), &example); err != nil {
		t.Fatalf("expected valid JSON example, got %q: %v", getOrder.ExampleJSON, err)
	}
	if example["status"] != "pending" {
		t.Fatalf("expected the spec's own example to be used, got %+v", example)
	}
	foundPathParam := false
	for _, p := range getOrder.Params {
		if p.In == "path" && p.Name == "id" && p.Required {
			foundPathParam = true
		}
	}
	if !foundPathParam {
		t.Fatalf("expected a required path param 'id', got %+v", getOrder.Params)
	}
}

func TestParseFixtureOpenAPIScaffoldsCreateOrder(t *testing.T) {
	byName := parseFixtureOps(t)
	createOrder, ok := byName["createOrder"]
	if !ok {
		t.Fatalf("expected a createOrder operation, got %+v", byName)
	}
	if createOrder.Method != "POST" || createOrder.StatusCode != 201 {
		t.Fatalf("unexpected createOrder method/status: %+v", createOrder)
	}
	if createOrder.ContentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", createOrder.ContentType)
	}
	foundBodyParam := false
	for _, p := range createOrder.Params {
		if p.In == "body" && p.Name == "customerName" && p.Required {
			foundBodyParam = true
		}
	}
	if !foundBodyParam {
		t.Fatalf("expected a required body param 'customerName', got %+v", createOrder.Params)
	}
	// No explicit example for createOrder's 201 response — must be
	// synthesized from its schema instead of an empty/missing body.
	var synthesized map[string]any
	if err := json.Unmarshal([]byte(createOrder.ExampleJSON), &synthesized); err != nil {
		t.Fatalf("expected valid synthesized JSON, got %q: %v", createOrder.ExampleJSON, err)
	}
	if _, ok := synthesized["status"]; !ok {
		t.Fatalf("expected a synthesized 'status' field from the schema, got %+v", synthesized)
	}
}

// TestParsePrependsServersBasePath guards against a real gap: a spec
// documenting `servers: [{url: ".../v2"}]` and a path of "/orders" describes
// a real endpoint at "/v2/orders" — scaffolding a mock at bare "/orders"
// would never actually match a real client hitting the documented base path.
func TestParsePrependsServersBasePath(t *testing.T) {
	const withBasePath = `{
	  "openapi": "3.0.0",
	  "info": {"title": "Orders API", "version": "1.0"},
	  "servers": [{"url": "https://api.example.com/v2"}],
	  "paths": {
	    "/orders": {
	      "get": {"operationId": "listOrders", "responses": {"200": {"description": "OK"}}}
	    }
	  }
	}`
	ops, err := Parse([]byte(withBasePath))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 || ops[0].Path != "/v2/orders" {
		t.Fatalf("expected path /v2/orders, got %+v", ops)
	}
}

// TestParsePrependsLeadingSlashForRelativeServerURL guards against a real
// gap found by a follow-up audit: a legal OpenAPI relative server URL with
// no leading slash (e.g. "v2", as opposed to "/v2") made url.Parse's .Path
// come back as "v2" with no leading slash, so the scaffolded path became
// "v2/orders" instead of "/v2/orders" — chi.Mux.MethodFunc panics on any
// route pattern not starting with '/', and registerRestRoute's per-mock
// recover swallowed that panic silently, so the mock was created (201
// reported) but never actually got a working route.
func TestParsePrependsLeadingSlashForRelativeServerURL(t *testing.T) {
	const relativeServerURL = `{
	  "openapi": "3.0.0",
	  "info": {"title": "Orders API", "version": "1.0"},
	  "servers": [{"url": "v2"}],
	  "paths": {
	    "/orders": {
	      "get": {"operationId": "listOrders", "responses": {"200": {"description": "OK"}}}
	    }
	  }
	}`
	ops, err := Parse([]byte(relativeServerURL))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 || ops[0].Path != "/v2/orders" {
		t.Fatalf("expected path /v2/orders, got %+v", ops)
	}
}

// TestParseFallsBackToNonJSONContentType guards against a real gap: a spec
// documenting only e.g. "application/xml" responses used to scaffold a mock
// with an empty "{}" body and a hardcoded application/json Content-Type,
// silently discarding the spec's actual declared response entirely.
func TestParseFallsBackToNonJSONContentType(t *testing.T) {
	const xmlOnly = `{
	  "openapi": "3.0.0",
	  "info": {"title": "Legacy API", "version": "1.0"},
	  "paths": {
	    "/status": {
	      "get": {
	        "operationId": "getStatus",
	        "responses": {
	          "200": {
	            "description": "OK",
	            "content": {"application/xml": {"example": "<status>ok</status>"}}
	          }
	        }
	      }
	    }
	  }
	}`
	ops, err := Parse([]byte(xmlOnly))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %+v", ops)
	}
	if ops[0].ContentType != "application/xml" {
		t.Fatalf("expected application/xml content type, got %q", ops[0].ContentType)
	}
	if ops[0].ExampleJSON == "{}" || ops[0].ExampleJSON == "" {
		t.Fatalf("expected the XML example to be carried over, got %q", ops[0].ExampleJSON)
	}
}
