package memory

import "testing"

// A Spanish question finds an English note through word matching: frequent
// technical words are looked up in English too (S9).
func TestLexicalSpanishQuestionFindsEnglishNote(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Deployment fix", Body: "The release failed because the database migration ran twice."}).ID
	save(t, s, SaveInput{Project: "p", Title: "Unrelated", Body: "notes about lunch"})

	r := search(t, s, SearchInput{Query: "¿Por qué falló el despliegue de la base de datos?", Project: "p"})
	if r.Mode != ModeLexical {
		t.Fatalf("mode = %q, want lexical", r.Mode)
	}
	if len(r.Hits) == 0 || r.Hits[0].ID != id {
		t.Errorf("hits = %v, want %d first", ids(r), id)
	}
	if r.Confidence != ConfidenceUnknown {
		t.Errorf("confidence = %q, want unknown", r.Confidence)
	}
}

// The Spanish words themselves are still searched, so Spanish notes keep
// matching, and plural Spanish forms are looked up as their singular.
func TestLexicalSpanishKeepsOriginalWordsAndPlurals(t *testing.T) {
	s := newSvc(t)
	es := save(t, s, SaveInput{Project: "p", Title: "Despliegue nocturno", Body: "lo hacemos de noche"}).ID
	en := save(t, s, SaveInput{Project: "p", Title: "Nightly deployments", Body: "we deploy at night"}).ID

	r := search(t, s, SearchInput{Query: "los despliegues", Project: "p", K: 5})
	if !contains(ids(r), en) {
		t.Errorf("hits = %v, want the English note %d (plural looked up)", ids(r), en)
	}
	r = search(t, s, SearchInput{Query: "el despliegue", Project: "p", K: 5})
	if !contains(ids(r), es) || !contains(ids(r), en) {
		t.Errorf("hits = %v, want both %d and %d", ids(r), es, en)
	}
}

// English queries are untouched: no English word gains a translation.
func TestFTSQueryLeavesEnglishAlone(t *testing.T) {
	cases := map[string]string{
		"rollout plan for the cache": `"rollout" OR "plan" OR "cache"`,
		"database migration error":   `"database" OR "migration" OR "error"`,
		"":                           "",
	}
	for q, want := range cases {
		if got := ftsQuery(q, true); got != want {
			t.Errorf("ftsQuery(%q) = %s, want %s", q, got, want)
		}
	}
}

// English keyword queries (no function words, no Spanish spelling) are not
// looked up, even when a word is or starts like a Spanish stem (B2).
func TestFTSQueryLeavesEnglishKeywordsAlone(t *testing.T) {
	cases := map[string]string{
		"patron":               `"patron"`,
		"precious metals":      `"precious" OR "metals"`,
		"indices rebuild":      `"indices" OR "rebuild"`,
		"eliminate duplicates": `"eliminate" OR "duplicates"`,
		"revision history":     `"revision" OR "history"`,
		"precision recall":     `"precision" OR "recall"`,
		"consultant notes":     `"consultant" OR "notes"`,
		"restaurant booking":   `"restaurant" OR "booking"`,
		"despliegues":          `"despliegues"`,
	}
	for q, want := range cases {
		if got := ftsQuery(q, true); got != want {
			t.Errorf("ftsQuery(%q) = %s, want %s", q, got, want)
		}
	}
}

// Spanish keywords with Spanish spelling, and Spanish sentences, are looked up.
func TestFTSQueryLooksUpSpanishMarkedKeywords(t *testing.T) {
	cases := map[string]string{
		"configuración":       `"configuracion" OR "configuration" OR "config"`,
		"los patrones":        `"patrones" OR "pattern"`,
		"¿precio?":            `"precio" OR "price" OR "pricing"`,
		"revisión del código": `"revision" OR "review" OR "codigo" OR "code"`,
	}
	for q, want := range cases {
		if got := ftsQuery(q, true); got != want {
			t.Errorf("ftsQuery(%q) = %s, want %s", q, got, want)
		}
	}
}

// An English keyword search returns what it returned before: a word that
// starts like a Spanish stem gains no unrelated hits (B2 regression).
func TestLexicalEnglishKeywordGainsNoTranslatedHits(t *testing.T) {
	s := newSvc(t)
	save(t, s, SaveInput{Project: "p", Title: "Pricing page", Body: "the price table moved"})
	save(t, s, SaveInput{Project: "p", Title: "Pattern matching", Body: "a pattern for routes"})

	for _, q := range []string{"precious", "patron"} {
		r := search(t, s, SearchInput{Query: q, Project: "p", K: 5})
		if len(r.Hits) != 0 {
			t.Errorf("search(%q) hits = %v, want none", q, ids(r))
		}
	}
}

func TestFTSQueryAddsEnglishForSpanish(t *testing.T) {
	got := ftsQuery("¿Cómo arreglamos la configuración?", true)
	want := `"arreglamos" OR "fix" OR "fixed" OR "configuracion" OR "configuration" OR "config"`
	if got != want {
		t.Errorf("ftsQuery = %s\nwant      %s", got, want)
	}
	if got := ftsQuery("¿Cómo arreglamos la configuración?", false); got != `"arreglamos" OR "configuracion"` {
		t.Errorf("ftsQuery without translate = %s, want the query's own words", got)
	}
}

// Hybrid search keeps its lexical list untranslated: the benchmark showed that
// translating it lowered Spanish hit@3 and MRR. Every text embeds to the same
// vector, so dense order is save order; an untranslated Spanish query matches no
// word, so the fused order is the dense order.
func TestHybridDoesNotTranslateSpanish(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: func(string) []float32 { return []float32{1, 0, 0} }})
	first := save(t, s, SaveInput{Project: "p", Title: "Unrelated", Body: "notes about lunch"}).ID
	en := save(t, s, SaveInput{Project: "p", Title: "Deployment fix", Body: "The release failed because the database migration ran twice."}).ID
	s.SetHybrid(true)

	r := search(t, s, SearchInput{Query: "¿Por qué falló el despliegue?", Project: "p", K: 5})
	if r.Mode != ModeHybrid {
		t.Fatalf("mode = %q, want hybrid", r.Mode)
	}
	if got := ids(r); len(got) != 2 || got[0] != first || got[1] != en {
		t.Errorf("hits = %v, want dense order [%d %d]", got, first, en)
	}
}
