package memory

import "testing"

func TestSearchWidensWhenProjectHasNoMemories(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	other := save(t, s, SaveInput{Project: "q", Type: "decision", Title: "Cache policy", Body: "cache with LRU"}).ID

	r := search(t, s, SearchInput{Query: "cache", Project: "empty-project"})
	if !r.Widened {
		t.Fatal("Widened = false, want true")
	}
	if len(r.Hits) != 1 || r.Hits[0].ID != other || r.Hits[0].Project != "q" {
		t.Errorf("hits = %+v, want #%d from project q", r.Hits, other)
	}
	if r.Confidence != ConfidenceOK {
		t.Errorf("confidence = %q, want ok", r.Confidence)
	}
}

func TestSearchWidensWhenProjectOnlyHasOffTopicMemories(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	save(t, s, SaveInput{Project: "p", Title: "Auth model", Body: "auth tokens"})
	other := save(t, s, SaveInput{Project: "q", Title: "Cache policy", Body: "cache with LRU"}).ID

	r := search(t, s, SearchInput{Query: "cache", Project: "p"})
	if !r.Widened || len(r.Hits) == 0 || r.Hits[0].ID != other {
		t.Errorf("widened=%v hits=%+v, want widened with #%d first", r.Widened, r.Hits, other)
	}
}

func TestSearchDoesNotWidenWhenProjectAnswers(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	mine := save(t, s, SaveInput{Project: "p", Title: "Cache policy", Body: "cache with LRU"}).ID
	save(t, s, SaveInput{Project: "q", Title: "Cache elsewhere", Body: "cache in another project"})

	r := search(t, s, SearchInput{Query: "cache", Project: "p"})
	if r.Widened {
		t.Error("Widened = true, want false")
	}
	if got := ids(r); len(got) != 1 || got[0] != mine {
		t.Errorf("hits = %v, want only #%d", got, mine)
	}
}

func TestSearchNothingAnywhereStaysNotWidened(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	save(t, s, SaveInput{Project: "q", Title: "Auth model", Body: "auth tokens"})

	r := search(t, s, SearchInput{Query: "weather forecast", Project: "p"})
	if r.Widened || r.Confidence != ConfidenceLow {
		t.Errorf("widened=%v confidence=%q, want not widened and low", r.Widened, r.Confidence)
	}
}
