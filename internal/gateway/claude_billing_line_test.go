package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// billingLine is the line Claude Code leads its system prompt with, as
// 2.1.114 sends it: its attribution fields, the last of them cch, a hash of
// the request's first user message that changes with every turn.
const billingLine = "x-anthropic-billing-header: cc_version=2.1.114.aff; cc_entrypoint=sdk-cli; cch=0218c;"

// claudeBillingTurn is a turn as Claude Code sends it: its system prompt
// led by billingLine, a second block after it.
func claudeBillingTurn(model string) string {
	return `{"model":"` + model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}],` +
		`"system":[{"type":"text","text":` +
		quote(billingLine+"\nYou are Claude Code, Anthropic's official CLI for Claude.") +
		`},{"type":"text","text":"The rules."}]}`
}

// A request magpie rebuilds for another API is sent the prompt Claude Code
// gave it without the billing line, which is prompt text there: its hash
// follows the turn, so the vendor's prompt cache matched nothing and every
// turn was billed whole (#1600). One relayed to a vendor that speaks
// Anthropic alone goes out as Claude Code sent it (#179).
func TestClaudeBillingLineGoesFromRebuiltRequests(t *testing.T) {
	send := func(t *testing.T, proto provider.Protocol, reply string) *fake {
		t.Helper()
		f := &fake{t: t, reply: reply}
		if proto == provider.Anthropic {
			// relayed as it came: the vendor's own answer, not a stream
			// magpie reads
			f.ctype = "application/json"
		}
		setup(t, proto, f)
		p, _ := provider.Find("fake")
		p.Models = []string{"m"}
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(claudeBillingTurn("fake/m")))
		req.Header.Set("User-Agent", "claude-cli/2.1.114 (external, cli)")
		req.Header.Set("Authorization", "Bearer "+Token)
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%v: %d %s", proto, rec.Code, rec.Body)
		}
		return f
	}

	chat := sse(
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`)
	responses := sse(
		`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"hi"}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","usage":{"input_tokens":5,"output_tokens":2}}}`)
	rebuilt := []struct {
		to    provider.Protocol
		reply string
		where string
	}{
		{provider.Chat, chat, "messages.0.content"},
		{provider.Responses, responses, "instructions"},
	}
	for _, c := range rebuilt {
		f := send(t, c.to, c.reply)
		got := gjson.GetBytes(f.got, c.where).String()
		if strings.Contains(got, "x-anthropic-billing-header:") {
			t.Errorf("%v: the billing line reached the vendor: %s", c.to, f.got)
		}
		if !strings.Contains(got, "Anthropic's official CLI for Claude.") || !strings.Contains(got, "The rules.") {
			t.Errorf("%v: the prompt didn't reach the vendor: %q", c.to, got)
		}
	}

	// Anthropic alone: the request is relayed as it came, the line on it
	f := send(t, provider.Anthropic, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`)
	if !strings.Contains(string(f.got), billingLine) {
		t.Errorf("the relayed request lost the line: %s", f.got)
	}
}

// A Command Code Go account is asked at the CLI's own /alpha/generate with
// the prompt itself, the line leading it: it goes from there too (#1600).
func TestCommandCodeGoDropsTheBillingLine(t *testing.T) {
	u := newCmdUpstream(t, cmdLines(`{"type":"text-delta","text":"from generate"}`, cmdFinish))
	status, msg, out, call := cmdAttempt(t, cmdAccount(u, "go"), provider.Anthropic, "deepseek/deepseek-v4-pro", claudeBillingTurn("deepseek/deepseek-v4-pro"))
	if status != 200 || !strings.Contains(out, "from generate") || call.To != provider.Anthropic {
		t.Fatalf("%d %q %s", status, msg, out)
	}
	system := gjson.GetBytes(u.body, "params.system").String()
	if strings.Contains(system, "x-anthropic-billing-header:") {
		t.Fatalf("the billing line reached Command Code: %q", system)
	}
	if !strings.Contains(system, "Anthropic's official CLI for Claude.") || !strings.Contains(system, "The rules.") {
		t.Fatalf("the prompt didn't reach Command Code: %q", system)
	}
}
