package store_test

// The queues are where the move removes a lock rather than a table: the SQL
// claimed a row with SELECT ... FOR UPDATE inside a transaction, and the MongoDB
// version claims it with the filter itself. What must hold either way is that no
// message is handed to two claimants, and that the ordering and the scoping
// filters are the same — so the tests here run against a real server, where
// concurrency and filters are decided.

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestPostInbound_ClaimInbound(t *testing.T) {
	s := storetest.Connect(t)

	posted := nanoprotocol.InboundMessage{
		UserID: "u-1", AgentID: "agent-1", SessionID: "s-1", Channel: "telegram",
		ChatID: "c-1", SenderID: "sender-1", MessageID: "m-1", Content: "hello",
		Media:    []nanoprotocol.MediaItem{{Platform: "wecom", MediaID: "mid-9", Filename: "a.png", MimeType: "image/png", URL: "file:///x"}},
		Metadata: map[string]any{"chat_type": "group"},
	}
	if err := s.PostInbound(posted); err != nil {
		t.Fatalf("PostInbound: %v", err)
	}

	got, err := s.ClaimInbound("u-1", 0, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got == nil {
		t.Fatal("nothing was claimed")
	}
	if got.UserID != "u-1" || got.AgentID != "agent-1" || got.SessionID != "s-1" ||
		got.Channel != "telegram" || got.ChatID != "c-1" || got.SenderID != "sender-1" ||
		got.MessageID != "m-1" || got.Content != "hello" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if got.Timestamp.IsZero() {
		t.Fatal("the message carries no timestamp")
	}
	if len(got.Media) != 1 || got.Media[0].MediaID != "mid-9" || got.Media[0].MimeType != "image/png" {
		t.Fatalf("media lost fields: %+v", got.Media)
	}
	if got.Metadata["chat_type"] != "group" {
		t.Fatalf("metadata lost: %+v", got.Metadata)
	}

	// It is claimed, so a second claimant finds nothing.
	again, err := s.ClaimInbound("u-1", 0, "")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if again != nil {
		t.Fatalf("the same message was claimed twice: %+v", again)
	}
}

func TestClaimInbound_Empty(t *testing.T) {
	s := storetest.Connect(t)

	got, err := s.ClaimInbound("u-1", 0, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nothing to claim, got %+v", got)
	}
}

func TestClaimInbound_OldestFirst(t *testing.T) {
	s := storetest.Connect(t)

	for _, content := range []string{"first", "second", "third"} {
		if err := s.PostInbound(nanoprotocol.InboundMessage{
			UserID: "u-1", SessionID: "s-1", Channel: "telegram", ChatID: "c-1",
			SenderID: "x", Content: content,
		}); err != nil {
			t.Fatalf("PostInbound: %v", err)
		}
	}

	for _, want := range []string{"first", "second", "third"} {
		got, err := s.ClaimInbound("u-1", 0, "")
		if err != nil {
			t.Fatalf("ClaimInbound: %v", err)
		}
		if got == nil || got.Content != want {
			t.Fatalf("expected %q, got %+v", want, got)
		}
	}
}

// TestClaimInbound_BindingScoping pins the SQL's `binding_id = ? OR binding_id IS
// NULL`: a claim for one binding also picks up messages that carry none, and
// never one that belongs to another binding.
func TestClaimInbound_BindingScoping(t *testing.T) {
	s := storetest.Connect(t)

	post := func(content string, bindingID int64) {
		t.Helper()
		if err := s.PostInbound(nanoprotocol.InboundMessage{
			UserID: "u-1", BindingID: bindingID, SessionID: "s-1", Channel: "telegram",
			ChatID: "c-1", SenderID: "x", Content: content,
		}); err != nil {
			t.Fatalf("PostInbound: %v", err)
		}
	}
	post("unbound", 0)
	post("binding-2", 2)
	post("binding-1", 1)

	// A claim for binding 1 takes the unbound message first (it is older).
	got, err := s.ClaimInbound("u-1", 1, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got == nil || got.Content != "unbound" {
		t.Fatalf("expected the unbound message, got %+v", got)
	}
	got, err = s.ClaimInbound("u-1", 1, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got == nil || got.Content != "binding-1" {
		t.Fatalf("expected the message of binding 1, got %+v", got)
	}
	// Binding 2's message is not this binding's to take.
	got, err = s.ClaimInbound("u-1", 1, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got != nil {
		t.Fatalf("a claim for binding 1 took another binding's message: %+v", got)
	}
	// A claim with no binding takes whatever is left.
	got, err = s.ClaimInbound("u-1", 0, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got == nil || got.Content != "binding-2" {
		t.Fatalf("an unscoped claim should take the remaining message, got %+v", got)
	}
}

// TestClaimInbound_AgentScoping pins `agent_id = ” OR agent_id = ?`: messages
// addressed to nobody in particular belong to whoever asks.
func TestClaimInbound_AgentScoping(t *testing.T) {
	s := storetest.Connect(t)

	post := func(content, agentID string) {
		t.Helper()
		if err := s.PostInbound(nanoprotocol.InboundMessage{
			UserID: "u-1", AgentID: agentID, SessionID: "s-1", Channel: "telegram",
			ChatID: "c-1", SenderID: "x", Content: content,
		}); err != nil {
			t.Fatalf("PostInbound: %v", err)
		}
	}
	post("unaddressed", "")
	post("for-b", "agent-b")
	post("for-a", "agent-a")

	got, err := s.ClaimInbound("u-1", 0, "agent-a")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got == nil || got.Content != "unaddressed" {
		t.Fatalf("expected the unaddressed message first, got %+v", got)
	}
	got, err = s.ClaimInbound("u-1", 0, "agent-a")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got == nil || got.Content != "for-a" {
		t.Fatalf("expected the message addressed to agent-a, got %+v", got)
	}
	got, err = s.ClaimInbound("u-1", 0, "agent-a")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got != nil {
		t.Fatalf("agent-a took a message addressed to agent-b: %+v", got)
	}
	// And the user's messages are scoped to that user only.
	if err := s.PostInbound(nanoprotocol.InboundMessage{
		UserID: "u-2", SessionID: "s-2", Channel: "telegram", ChatID: "c-2",
		SenderID: "y", Content: "other-user",
	}); err != nil {
		t.Fatalf("PostInbound: %v", err)
	}
	got, err = s.ClaimInbound("u-1", 0, "agent-a")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got != nil {
		t.Fatalf("another user's message leaked into the claim: %+v", got)
	}
}

// TestClaimInbound_ConcurrentClaimants is V-8 in the plan: the claim is one
// atomic operation, so a message is handed out once no matter how many pollers
// race for it.
func TestClaimInbound_ConcurrentClaimants(t *testing.T) {
	s := storetest.Connect(t)

	const messages = 20
	for i := 0; i < messages; i++ {
		if err := s.PostInbound(nanoprotocol.InboundMessage{
			UserID: "u-1", SessionID: "s-1", Channel: "telegram", ChatID: "c-1",
			SenderID: "x", Content: fmt.Sprintf("m-%d", i),
		}); err != nil {
			t.Fatalf("PostInbound: %v", err)
		}
	}

	const claimers = 8
	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				msg, err := s.ClaimInbound("u-1", 0, "")
				if err != nil {
					t.Errorf("ClaimInbound: %v", err)
					return
				}
				if msg == nil {
					return
				}
				mu.Lock()
				claimed[msg.Content]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(claimed) != messages {
		t.Fatalf("claimed %d of %d messages", len(claimed), messages)
	}
	for content, n := range claimed {
		if n != 1 {
			t.Fatalf("message %q was handed to %d claimants", content, n)
		}
	}
}

// TestClaimInbound_MessageIDFromMetadata: senders that do not carry a message id
// as a field have it taken out of the metadata.
func TestClaimInbound_MessageIDFromMetadata(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.PostInbound(nanoprotocol.InboundMessage{
		UserID: "u-1", SessionID: "s-1", Channel: "telegram", ChatID: "c-1",
		SenderID: "x", Content: "hi", Metadata: map[string]any{"msg_id": "platform-7"},
	}); err != nil {
		t.Fatalf("PostInbound: %v", err)
	}

	got, err := s.ClaimInbound("u-1", 0, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if got.MessageID != "platform-7" {
		t.Fatalf("MessageID = %q, want the metadata's id", got.MessageID)
	}
}

// TestClaimInbound_MetadataStaysJSON pins the collection's DefaultDocumentM: the
// metadata is passed straight through to the wire, so a nested value has to
// marshal as an object rather than as a list of key/value pairs.
func TestClaimInbound_MetadataStaysJSON(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.PostInbound(nanoprotocol.InboundMessage{
		UserID: "u-1", SessionID: "s-1", Channel: "telegram", ChatID: "c-1",
		SenderID: "x", Content: "hi",
		Metadata: map[string]any{"chat_type": "group", "nested": map[string]any{"n": float64(2)}},
	}); err != nil {
		t.Fatalf("PostInbound: %v", err)
	}

	got, err := s.ClaimInbound("u-1", 0, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	encoded, err := json.Marshal(got.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if string(encoded) != `{"chat_type":"group","nested":{"n":2}}` {
		t.Fatalf("metadata does not round trip as JSON: %s", encoded)
	}
}

func TestPostOutbound_NeedsAMessageID(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.PostOutbound(nanoprotocol.OutboundMessage{
		UserID: "u-1", SessionID: "s-1", Channel: "telegram", ChatID: "c-1", Content: "hi",
	}); err == nil {
		t.Fatal("an outbound message without a message id was accepted")
	}
}

func TestPostOutbound_ClaimOutbound(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.PostOutbound(nanoprotocol.OutboundMessage{
		UserID: "u-1", AgentID: "agent-1", SessionID: "s-1", Channel: "telegram",
		ChatID: "c-1", MessageID: "m-1", Content: "reply", ReplyTo: "m-0",
		Media:    []nanoprotocol.MediaItem{{URL: "file:///y", MimeType: "image/png"}},
		Metadata: map[string]any{"chat_type": "group"},
	}); err != nil {
		t.Fatalf("PostOutbound: %v", err)
	}

	got, err := s.ClaimOutbound("telegram", "gateway-1")
	if err != nil {
		t.Fatalf("ClaimOutbound: %v", err)
	}
	if got == nil {
		t.Fatal("nothing was claimed")
	}
	if got.UserID != "u-1" || got.AgentID != "agent-1" || got.SessionID != "s-1" ||
		got.Channel != "telegram" || got.ChatID != "c-1" || got.MessageID != "m-1" ||
		got.Content != "reply" || got.ReplyTo != "m-0" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if len(got.Media) != 1 || got.Media[0].URL != "file:///y" {
		t.Fatalf("media lost fields: %+v", got.Media)
	}
	if got.Metadata["chat_type"] != "group" {
		t.Fatalf("metadata lost: %+v", got.Metadata)
	}

	again, err := s.ClaimOutbound("telegram", "gateway-1")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if again != nil {
		t.Fatalf("the same message was claimed twice: %+v", again)
	}
}

// TestClaimOutbound_ChannelScoped: a gateway only takes its own channel's
// traffic, oldest first.
func TestClaimOutbound_ChannelScoped(t *testing.T) {
	s := storetest.Connect(t)

	post := func(channel, content string) {
		t.Helper()
		if err := s.PostOutbound(nanoprotocol.OutboundMessage{
			UserID: "u-1", SessionID: "s-1", Channel: channel, ChatID: "c-1",
			MessageID: "m-" + content, Content: content,
		}); err != nil {
			t.Fatalf("PostOutbound: %v", err)
		}
	}
	post("telegram", "tg-first")
	post("feishu", "fs-first")
	post("telegram", "tg-second")

	for _, want := range []string{"tg-first", "tg-second"} {
		got, err := s.ClaimOutbound("telegram", "gw")
		if err != nil {
			t.Fatalf("ClaimOutbound: %v", err)
		}
		if got == nil || got.Content != want {
			t.Fatalf("expected %q, got %+v", want, got)
		}
	}
	if got, err := s.ClaimOutbound("telegram", "gw"); err != nil || got != nil {
		t.Fatalf("expected the telegram queue to be empty, got %+v (err %v)", got, err)
	}
	got, err := s.ClaimOutbound("feishu", "gw")
	if err != nil {
		t.Fatalf("ClaimOutbound: %v", err)
	}
	if got == nil || got.Content != "fs-first" {
		t.Fatalf("the other channel's message should be untouched, got %+v", got)
	}
}

func TestMarkOutboundDone(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.PostOutbound(nanoprotocol.OutboundMessage{
		UserID: "u-1", SessionID: "s-1", Channel: "telegram", ChatID: "c-1",
		MessageID: "m-1", Content: "reply",
	}); err != nil {
		t.Fatalf("PostOutbound: %v", err)
	}

	if err := s.MarkOutboundDone("m-1"); err != nil {
		t.Fatalf("MarkOutboundDone: %v", err)
	}
	// A delivered message is not claimable any more.
	got, err := s.ClaimOutbound("telegram", "gw")
	if err != nil {
		t.Fatalf("ClaimOutbound: %v", err)
	}
	if got != nil {
		t.Fatalf("a delivered message was claimed: %+v", got)
	}
	// Marking an unknown id is not an error, as with the SQL UPDATE.
	if err := s.MarkOutboundDone("nobody"); err != nil {
		t.Fatalf("MarkOutboundDone on a missing id: %v", err)
	}
}

// TestClaimOutbound_ConcurrentClaimants: the same guarantee as the inbound side,
// for the gateway's poll.
func TestClaimOutbound_ConcurrentClaimants(t *testing.T) {
	s := storetest.Connect(t)

	const messages = 20
	for i := 0; i < messages; i++ {
		if err := s.PostOutbound(nanoprotocol.OutboundMessage{
			UserID: "u-1", SessionID: "s-1", Channel: "telegram", ChatID: "c-1",
			MessageID: fmt.Sprintf("m-%d", i), Content: fmt.Sprintf("m-%d", i),
		}); err != nil {
			t.Fatalf("PostOutbound: %v", err)
		}
	}

	const claimers = 8
	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				msg, err := s.ClaimOutbound("telegram", "gw")
				if err != nil {
					t.Errorf("ClaimOutbound: %v", err)
					return
				}
				if msg == nil {
					return
				}
				mu.Lock()
				claimed[msg.MessageID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(claimed) != messages {
		t.Fatalf("claimed %d of %d messages", len(claimed), messages)
	}
	for messageID, n := range claimed {
		if n != 1 {
			t.Fatalf("message %q was handed to %d claimants", messageID, n)
		}
	}
}

func TestMigrations_DeclareQueueIndexes(t *testing.T) {
	s := storetest.Connect(t)

	// The claim filters on (user_id, status) and sorts by (created_at, _id), so
	// all four are in the key — the trailing _id included, or the sort would not
	// be served by the index.
	assertIndexKeys(t, s.DB().Collection("inbound_queue"), "user_pending", [][2]any{
		{"user_id", int32(1)}, {"status", int32(1)}, {"created_at", int32(1)}, {"_id", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("inbound_queue"), "agent_pending", [][2]any{
		{"agent_id", int32(1)}, {"status", int32(1)}, {"created_at", int32(1)}, {"_id", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("outbound_queue"), "channel_pending", [][2]any{
		{"channel", int32(1)}, {"status", int32(1)}, {"created_at", int32(1)}, {"_id", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("outbound_queue"), "user_pending", [][2]any{
		{"user_id", int32(1)}, {"status", int32(1)}, {"created_at", int32(1)}, {"_id", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("outbound_queue"), "agent_pending", [][2]any{
		{"agent_id", int32(1)}, {"status", int32(1)}, {"created_at", int32(1)}, {"_id", int32(1)},
	})
}
