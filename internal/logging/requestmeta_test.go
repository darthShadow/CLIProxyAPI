package logging

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestWithFreshResponseHeadersHolderIsolatesAttempts(t *testing.T) {
	requestCtx := WithResponseHeadersHolder(context.Background())
	SetResponseHeaders(requestCtx, http.Header{"X-Upstream-Attempt": []string{"request"}})

	firstAttemptCtx := WithFreshResponseHeadersHolder(requestCtx)
	if headers := GetResponseHeaders(firstAttemptCtx); len(headers) != 0 {
		t.Fatalf("fresh attempt inherited request headers: %#v", headers)
	}
	SetResponseHeaders(firstAttemptCtx, http.Header{"X-Upstream-Attempt": []string{"first"}})

	secondAttemptCtx := WithFreshResponseHeadersHolder(firstAttemptCtx)
	if headers := GetResponseHeaders(secondAttemptCtx); len(headers) != 0 {
		t.Fatalf("second attempt inherited first attempt headers: %#v", headers)
	}
	SetResponseHeaders(secondAttemptCtx, http.Header{"X-Upstream-Attempt": []string{"second"}})

	if got := GetResponseHeaders(requestCtx).Get("X-Upstream-Attempt"); got != "request" {
		t.Fatalf("request holder = %q, want request", got)
	}
	if got := GetResponseHeaders(firstAttemptCtx).Get("X-Upstream-Attempt"); got != "first" {
		t.Fatalf("first attempt holder = %q, want first", got)
	}
	if got := GetResponseHeaders(secondAttemptCtx).Get("X-Upstream-Attempt"); got != "second" {
		t.Fatalf("second attempt holder = %q, want second", got)
	}
}

func TestResponseHeaderTimesFollowEachKeyWrite(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		if h, times := GetResponseHeadersObserved(ctx); h != nil || times != nil {
			t.Fatalf("without holder: headers=%v times=%v, want nil maps", h, times)
		}
	}
	ctx := WithResponseHeadersHolder(context.Background())
	if h, times := GetResponseHeadersObserved(ctx); h != nil || times != nil {
		t.Fatalf("fresh holder: headers=%v times=%v, want nil maps", h, times)
	}
	beforeSet := time.Now()
	SetResponseHeaders(ctx, http.Header{"X-A": {"a"}, "x-b": {"b"}})
	afterSet := time.Now()
	headers, setTimes := GetResponseHeadersObserved(ctx)
	if len(setTimes) != 2 {
		t.Fatalf("Set time map=%v, want two keys", setTimes)
	}
	for _, key := range []string{"X-A", "X-B"} {
		at := setTimes[key]
		if at.Before(beforeSet) || at.After(afterSet) {
			t.Errorf("%s Set time=%v, bounds=[%v,%v]", key, at, beforeSet, afterSet)
		}
	}
	if !setTimes["X-A"].Equal(setTimes["X-B"]) {
		t.Fatal("Set keys have different times")
	}
	MergeResponseHeaders(ctx, nil)
	_, emptyTimes := GetResponseHeadersObserved(ctx)
	if !reflect.DeepEqual(setTimes, emptyTimes) {
		t.Fatalf("empty merge times=%v, want %v", emptyTimes, setTimes)
	}
	beforeMerge := time.Now()
	MergeResponseHeaders(ctx, http.Header{"X-B": {"new"}, "x-c": {"c"}})
	afterMerge := time.Now()
	merged, times := GetResponseHeadersObserved(ctx)
	if len(times) != 3 || !times["X-A"].Equal(setTimes["X-A"]) {
		t.Fatalf("unchanged key lost: %v", times)
	}
	for _, key := range []string{"X-B", "X-C"} {
		at := times[key]
		if at.Before(beforeMerge) || at.After(afterMerge) || at.Before(setTimes["X-B"]) {
			t.Errorf("%s merge time=%v outside bounds", key, at)
		}
	}
	if !times["X-B"].Equal(times["X-C"]) {
		t.Fatal("merged keys have different times")
	}
	if !reflect.DeepEqual(headers, http.Header{"X-A": {"a"}, "x-b": {"b"}}) {
		t.Fatalf("Set snapshot changed: %v", headers)
	}
	merged["X-A"][0] = "mutated"
	times["X-A"] = time.Time{}
	again, againTimes := GetResponseHeadersObserved(ctx)
	if again.Get("X-A") != "a" || !againTimes["X-A"].Equal(setTimes["X-A"]) {
		t.Fatal("getter aliases holder")
	}
	SetResponseHeaders(ctx, http.Header{"X-D": {"d"}})
	_, replaced := GetResponseHeadersObserved(ctx)
	if len(replaced) != 1 || replaced["X-D"].IsZero() {
		t.Fatalf("replacement times=%v", replaced)
	}
	SetResponseHeaders(ctx, nil)
	_, cleared := GetResponseHeadersObserved(ctx)
	if len(cleared) != 0 {
		t.Fatalf("empty Set retains times: %v", cleared)
	}
}
