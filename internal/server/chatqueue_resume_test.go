package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// drainEvents 非阻塞地取走 sink 上此刻已到达的全部事件。
//
// 用非阻塞读（default 分支）而不是阻塞读：这些断言的语义是"该到的都已经
// 到了"，用阻塞读会把用例挂死在一次失败的投递上，看到的是超时而不是断言。
func drainEvents(snk *turnSink) []turnEvent {
	var out []turnEvent
	for {
		select {
		case ev, ok := <-snk.evch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// TestEventSeqIsMonotonic 每条广播的事件都拿到严格递增的序号，监听者收到的
// 事件的 seq 与分配顺序一致。序号是断线续传的唯一坐标，一旦重复或跳变，
// 客户端报回来的位点就无法定位。
func TestEventSeqIsMonotonic(t *testing.T) {
	q := newSessionQueue()
	snk := q.addSink()
	defer q.closeSink(snk)

	for i := 1; i <= 5; i++ {
		q.broadcast(turnEvent{data: fmt.Sprintf("data: {\"i\":%d}\n\n", i)})
	}

	evs := drainEvents(snk)
	if len(evs) != 5 {
		t.Fatalf("want 5 events, got %d", len(evs))
	}
	for i, ev := range evs {
		if ev.seq != uint64(i+1) {
			t.Fatalf("event %d: want seq %d, got %d", i, i+1, ev.seq)
		}
	}
}

// TestAddSinkFromReplaysMissedEvents 断线期间产生的事件在重连时必须补齐。
// 这就是"手机切后台回来，回答从断点接上"的核心保证——只靠 /running 对账
// 最多恢复到最近一次 checkpoint（服务端每 3 秒才落一次），中间的增量文本
// 永久丢失。
func TestAddSinkFromReplaysMissedEvents(t *testing.T) {
	q := newSessionQueue()

	// 第一条连接：收到前两条后断开。
	first := q.addSink()
	q.broadcast(turnEvent{data: "data: {\"delta\":\"a\"}\n\n"})
	q.broadcast(turnEvent{data: "data: {\"delta\":\"b\"}\n\n"})
	got := drainEvents(first)
	if len(got) != 2 {
		t.Fatalf("first sink: want 2 events, got %d", len(got))
	}
	lastSeq := got[len(got)-1].seq
	q.closeSink(first) // 断线

	// 断线期间服务端继续产出。
	q.broadcast(turnEvent{data: "data: {\"delta\":\"c\"}\n\n"})
	q.broadcast(turnEvent{data: "data: {\"delta\":\"d\"}\n\n"})

	// 重连：报"我已收到 lastSeq 为止"，应当只补回 c、d。
	second, resync := q.addSinkFrom(lastSeq)
	defer q.closeSink(second)
	if resync {
		t.Fatalf("unexpected resync for a still-available offset")
	}
	replayed := drainEvents(second)
	if len(replayed) != 2 {
		t.Fatalf("want 2 replayed events, got %d", len(replayed))
	}
	if replayed[0].seq != lastSeq+1 || replayed[1].seq != lastSeq+2 {
		t.Fatalf("replayed seqs = %d,%d; want %d,%d",
			replayed[0].seq, replayed[1].seq, lastSeq+1, lastSeq+2)
	}
}

// TestReplayPrecedesLiveEvents 补发的事件必须排在实时事件之前。顺序一旦
// 颠倒，客户端会先看到新 delta 再看到旧 delta，正文顺序彻底错乱——补发
// 因此在 sinkMu 临界区内完成，而不是在锁外先补后接。
func TestReplayPrecedesLiveEvents(t *testing.T) {
	q := newSessionQueue()

	head := q.addSink()
	q.broadcast(turnEvent{data: "data: {\"delta\":\"1\"}\n\n"})
	q.broadcast(turnEvent{data: "data: {\"delta\":\"2\"}\n\n"})
	got := drainEvents(head)
	seq := got[len(got)-1].seq
	q.closeSink(head)

	q.broadcast(turnEvent{data: "data: {\"delta\":\"3\"}\n\n"}) // 断线期间产生

	second, _ := q.addSinkFrom(seq)
	defer q.closeSink(second)

	// 补发完成之后，实时事件紧跟着进来。
	q.broadcast(turnEvent{data: "data: {\"delta\":\"4\"}\n\n"})

	evs := drainEvents(second)
	if len(evs) != 2 {
		t.Fatalf("want 2 events (1 replayed + 1 live), got %d", len(evs))
	}
	if evs[0].seq != seq+1 || evs[1].seq != seq+2 {
		t.Fatalf("order broken: seqs = %d,%d; want %d,%d",
			evs[0].seq, evs[1].seq, seq+1, seq+2)
	}
}

// TestAddSinkFromResyncsWhenOffsetEvicted 位点已被日志裁剪掉之后必须明确
// 回 resync，而不是从日志起点悄悄补一段残缺内容——后者会让前端把残缺的
// 回答当成完整的，用户看到"中间少了一段"且永远补不回来。
func TestAddSinkFromResyncsWhenOffsetEvicted(t *testing.T) {
	q := newSessionQueue()

	old := q.addSink()
	q.broadcast(turnEvent{data: "data: {\"delta\":\"old\"}\n\n"})
	got := drainEvents(old)
	staleSeq := got[0].seq
	q.closeSink(old)

	// 灌入远超日志上限的事件，把 staleSeq 挤出窗口。
	for i := 0; i < turnEventLogSize*3; i++ {
		q.broadcast(turnEvent{data: "data: {\"delta\":\"x\"}\n\n"})
	}

	if oldest := q.eventLogOldest(); oldest <= staleSeq {
		t.Fatalf("expected the stale offset to be evicted: oldest=%d stale=%d", oldest, staleSeq)
	}

	snk, resync := q.addSinkFrom(staleSeq)
	defer q.closeSink(snk)
	if !resync {
		t.Fatalf("expected resync=true for an evicted offset")
	}
	// resync 时绝不顺带补一个残缺前缀：前端要靠 /messages 重建整轮，
	// 半截补发只会让两边的状态更难对齐。
	if evs := drainEvents(snk); len(evs) != 0 {
		t.Fatalf("resync must not also replay a partial prefix, got %d events", len(evs))
	}
}

// TestAddSinkFromResyncsWhenOffsetAheadOfStream 队列被回收重建后序号归零，
// 客户端手里的旧位点比服务端还新——必须 resync。否则它会一直等一个永远
// 不会出现的序号区间，表现为"连接着、但一个字都不来"。
func TestAddSinkFromResyncsWhenOffsetAheadOfStream(t *testing.T) {
	q := newSessionQueue()
	snk, resync := q.addSinkFrom(99)
	defer q.closeSink(snk)
	if !resync {
		t.Fatalf("expected resync=true when the offset is ahead of the stream")
	}
}

// TestAddSinkFromFreshConnectionDoesNotReplay 全新连接（since=0）不补历史，
// 否则每次打开会话都会把上一轮的事件重放一遍，正文出现重复段落。
func TestAddSinkFromFreshConnectionDoesNotReplay(t *testing.T) {
	q := newSessionQueue()

	first := q.addSink()
	q.broadcast(turnEvent{data: "data: {\"delta\":\"a\"}\n\n"})
	drainEvents(first)
	q.closeSink(first)

	snk, resync := q.addSinkFrom(0)
	defer q.closeSink(snk)
	if resync {
		t.Fatalf("a fresh connection must not resync")
	}
	if evs := drainEvents(snk); len(evs) != 0 {
		t.Fatalf("a fresh connection must not replay history, got %d events", len(evs))
	}
}

// TestEventLogStaysWithinCap 长回合下事件日志必须有界，且裁剪后字节计数
// 必须仍然准确（算错会让此后每条事件都触发一次全量重算，退化成 O(n²)），
// 序号也必须保持连续——留空洞会让补发判定把"有缺失"误判成"无缺失"。
func TestEventLogStaysWithinCap(t *testing.T) {
	q := newSessionQueue()

	const payload = "data: {\"delta\":\"payload\"}\n\n"
	total := turnEventLogSize * 3
	for i := 0; i < total; i++ {
		q.broadcast(turnEvent{data: payload})
	}

	q.sinkMu.Lock()
	n, byteCount := len(q.evlog), q.evlogBytes
	seqs := make([]uint64, len(q.evlog))
	for i, ev := range q.evlog {
		seqs[i] = ev.seq
	}
	q.sinkMu.Unlock()

	if n > turnEventLogSize {
		t.Fatalf("event log grew past its cap: %d > %d", n, turnEventLogSize)
	}
	if n == 0 {
		t.Fatalf("event log unexpectedly empty")
	}
	if want := n * len(payload); byteCount != want {
		t.Fatalf("evlogBytes = %d, want %d", byteCount, want)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("event log has a hole: %d -> %d", seqs[i-1], seqs[i])
		}
	}
	// 最旧的 seq 必须等于"总数 - 保留数 + 1"：说明丢的确实是最老的那批，
	// 而不是随机丢中间（随机丢会让连续序号断言在上一步就失败）。
	if want := uint64(total-n) + 1; seqs[0] != want {
		t.Fatalf("oldest retained seq = %d, want %d", seqs[0], want)
	}
}

// TestForwardTurnEventsEmitsIDs 转发的每一帧都带 SSE id，且与事件序号一致。
// 客户端完全依赖这个 id 来决定重连时从哪里续起。
func TestForwardTurnEventsEmitsIDs(t *testing.T) {
	q := newSessionQueue()
	snk, _ := q.addSinkFrom(0)
	defer q.closeSink(snk)

	q.broadcast(turnEvent{data: "data: {\"delta\":\"a\"}\n\n"})
	q.broadcast(turnEvent{data: "data: {\"delta\":\"b\"}\n\n", done: true, queueIdle: true})

	var frames []string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s := &Server{}
	s.forwardTurnEvents(ctx, func(f string) bool {
		frames = append(frames, f)
		return true
	}, snk)

	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d: %#v", len(frames), frames)
	}
	if !strings.HasPrefix(frames[0], "id: 1\n") {
		t.Fatalf("frame 0 missing id header: %q", frames[0])
	}
	if !strings.HasPrefix(frames[1], "id: 2\n") {
		t.Fatalf("frame 1 missing id header: %q", frames[1])
	}
}

// TestSSEFrameWithID 帧格式必须是 SSE 标准的 "id: N\n" 前缀，且空 data
// 绝不能产出"只有 id、没有内容"的帧。
//
// 为什么空 data 是危险的：客户端会把 id 当作位点推进，于是一条它从没看到
// 内容的事件也变成了"已收到"，重连时服务端从那里往后补——那条事件的正文
// 就永久消失了。
func TestSSEFrameWithID(t *testing.T) {
	got := sseFrameWithID(42, "data: {\"delta\":\"hi\"}\n\n")
	want := "id: 42\ndata: {\"delta\":\"hi\"}\n\n"
	if got != want {
		t.Fatalf("sseFrameWithID = %q, want %q", got, want)
	}
	if got := sseFrameWithID(7, ""); got != "" {
		t.Fatalf("empty data must not emit a frame, got %q", got)
	}
}

// TestParseSinceParam 非法位点一律按"全新连接"处理。宁可漏补（前端还有
// /running 对账兜底），也不能让一个畸形参数把客户端引到错误的位点上——
// 那样它会以为自己连续，从而永远不去对账。
func TestParseSinceParam(t *testing.T) {
	cases := []struct {
		url  string
		want uint64
	}{
		{"/api/sessions/s1/stream?attach=1", 0},
		{"/api/sessions/s1/stream?attach=1&since=0", 0},
		{"/api/sessions/s1/stream?attach=1&since=17", 17},
		{"/api/sessions/s1/stream?attach=1&since=", 0},
		{"/api/sessions/s1/stream?attach=1&since=abc", 0},
		{"/api/sessions/s1/stream?attach=1&since=-3", 0},
		{"/api/sessions/s1/stream?attach=1&since=%2012%20", 12},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, c.url, nil)
		if got := parseSinceParam(r); got != c.want {
			t.Fatalf("parseSinceParam(%q) = %d, want %d", c.url, got, c.want)
		}
	}
}

// TestFlushPendingEventsEmitsQueuedFrames 空闲附着路径上的预冲刷必须把已排队
// 的补发事件带 id 写出去。漏掉它，重连补回来的正文会一直滞留在 sink 缓冲里，
// 直到下一条实时事件才"顺带"流出来——用户看到的是"连上了，但一直没有内容"。
//
// 同时钉住非阻塞语义：sink 空时必须立刻返回，而不是挂在那里等一个可能永远
// 不来的事件（那会把一条空闲附着连接变成卡死的 handler goroutine）。
func TestFlushPendingEventsEmitsQueuedFrames(t *testing.T) {
	q := newSessionQueue()
	snk, _ := q.addSinkFrom(0)
	defer q.closeSink(snk)

	q.broadcast(turnEvent{data: "data: {\"delta\":\"a\"}\n\n"})
	q.broadcast(turnEvent{data: "data: {\"delta\":\"b\"}\n\n"})

	var frames []string
	if ok := flushPendingEvents(func(f string) bool {
		frames = append(frames, f)
		return true
	}, snk); !ok {
		t.Fatalf("flushPendingEvents reported a write failure")
	}
	if len(frames) != 2 {
		t.Fatalf("want 2 flushed frames, got %d", len(frames))
	}
	if !strings.HasPrefix(frames[0], "id: 1\n") || !strings.HasPrefix(frames[1], "id: 2\n") {
		t.Fatalf("flushed frames missing id headers: %#v", frames)
	}

	done := make(chan bool, 1)
	go func() {
		done <- flushPendingEvents(func(string) bool { return true }, snk)
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("flush on an empty sink must not report a failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flush on an empty sink blocked instead of returning immediately")
	}
}
