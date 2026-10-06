package traffic

import "testing"

const sampleHAR = `{
  "log": {
    "entries": [
      {
        "request": { "method": "GET", "url": "https://api.example.com/orders/123?verbose=true" },
        "response": {
          "status": 200,
          "headers": [],
          "content": { "mimeType": "application/json", "text": "{\"id\":123,\"status\":\"shipped\"}" }
        }
      },
      {
        "request": { "method": "GET", "url": "https://api.example.com/orders/123" },
        "response": {
          "status": 200,
          "content": { "mimeType": "application/json", "text": "{\"id\":123,\"status\":\"shipped-second-hit\"}" }
        }
      },
      {
        "request": { "method": "POST", "url": "https://api.example.com/orders" },
        "response": {
          "status": 201,
          "content": { "mimeType": "application/json", "text": "eyJvayI6dHJ1ZX0=", "encoding": "base64" }
        }
      }
    ]
  }
}`

func TestParseHAR(t *testing.T) {
	out, err := ParseHAR([]byte(sampleHAR))
	if err != nil {
		t.Fatalf("ParseHAR: %v", err)
	}
	// Two entries share GET /orders/123 (differ only by query) — expect one
	// scaffold, using the FIRST capture's body, not the second.
	if len(out) != 2 {
		t.Fatalf("expected 2 scaffolded mocks (deduped by method+path), got %d: %+v", len(out), out)
	}

	get := out[0]
	if get.Method != "GET" || get.Path != "/orders/123" {
		t.Fatalf("unexpected first scaffold: %+v", get)
	}
	if get.ResponseBody != `{"id":123,"status":"shipped"}` {
		t.Fatalf("expected first-capture body to win, got %q", get.ResponseBody)
	}

	post := out[1]
	if post.Method != "POST" || post.Path != "/orders" || post.StatusCode != 201 {
		t.Fatalf("unexpected second scaffold: %+v", post)
	}
	if post.ResponseBody != `{"ok":true}` {
		t.Fatalf("expected base64-decoded body, got %q", post.ResponseBody)
	}
}

func TestParseHAR_empty(t *testing.T) {
	if _, err := ParseHAR([]byte(`{"log":{"entries":[]}}`)); err == nil {
		t.Fatal("expected an error for a HAR file with no entries")
	}
}

const samplePostman = `{
  "item": [
    {
      "name": "Orders",
      "item": [
        {
          "name": "Get order",
          "request": { "method": "GET", "url": { "raw": "{{baseUrl}}/orders/123" } },
          "response": [
            {
              "code": 200,
              "body": "{\"id\":123}",
              "header": [{ "key": "Content-Type", "value": "application/json" }]
            }
          ]
        },
        {
          "name": "No example yet",
          "request": { "method": "GET", "url": { "raw": "{{baseUrl}}/orders/456" } }
        }
      ]
    }
  ]
}`

func TestParsePostmanExamples(t *testing.T) {
	out, err := ParsePostmanExamples([]byte(samplePostman))
	if err != nil {
		t.Fatalf("ParsePostmanExamples: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 scaffolded mock (the item with no saved example is skipped), got %d: %+v", len(out), out)
	}
	m := out[0]
	if m.Method != "GET" || m.Path != "/orders/123" || m.StatusCode != 200 {
		t.Fatalf("unexpected scaffold: %+v", m)
	}
	if m.ResponseHeaders["Content-Type"] != "application/json" {
		t.Fatalf("expected Content-Type to carry over, got %+v", m.ResponseHeaders)
	}
}

func TestParsePostmanExamples_none(t *testing.T) {
	if _, err := ParsePostmanExamples([]byte(`{"item":[]}`)); err == nil {
		t.Fatal("expected an error when no item has a saved example")
	}
}

func TestPathOnly(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/orders/123?x=1": "/orders/123",
		"{{baseUrl}}/orders/123":                 "/orders/123",
		"/orders/123":                            "/orders/123",
		"":                                       "",
	}
	for in, want := range cases {
		if got := pathOnly(in); got != want {
			t.Errorf("pathOnly(%q) = %q, want %q", in, got, want)
		}
	}
}
