package dump

import (
	"fmt"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

func TestShardCount(t *testing.T) {
	cpus := runtime.NumCPU()
	tests := []struct {
		name       string
		totalFiles int
		want       int
	}{
		{"zero files", 0, 1},
		{"one file", 1, 1},
		{"1000 files", 1000, 1},
		{"2000 files", 2000, 1},
		{"4000 files", 4000, min(cpus, 2)},
		{"13000 files", 13000, min(cpus, 6)},
		{"60000 files", 60000, min(cpus, 30)},
		{"100000 files", 100000, min(cpus, 50)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shardCount(tt.totalFiles)
			if got != tt.want {
				t.Errorf("shardCount(%d) = %d, want %d (NumCPU=%d)", tt.totalFiles, got, tt.want, cpus)
			}
			if got < 1 {
				t.Errorf("shardCount(%d) = %d, must be >= 1", tt.totalFiles, got)
			}
		})
	}
}

func TestSplitByHash(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g"}

	t.Run("3 groups deterministic", func(t *testing.T) {
		groups := splitByHash(names, 3)
		if len(groups) != 3 {
			t.Fatalf("expected 3 groups, got %d", len(groups))
		}
		total := 0
		for _, g := range groups {
			total += len(g)
		}
		if total != len(names) {
			t.Errorf("expected %d total items, got %d", len(names), total)
		}
		for _, name := range names {
			shard := shardForID(name, 3)
			if !slices.Contains(groups[shard], name) {
				t.Errorf("name %q should be in group %d, not found", name, shard)
			}
		}
	})

	t.Run("1 group", func(t *testing.T) {
		groups := splitByHash(names, 1)
		if len(groups[0]) != len(names) {
			t.Errorf("single group should have all items")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		groups := splitByHash(nil, 3)
		for i, g := range groups {
			if len(g) != 0 {
				t.Errorf("group %d should be empty", i)
			}
		}
	})

	t.Run("consistent with shardForID", func(t *testing.T) {
		manyNames := make([]string, 100)
		for i := range manyNames {
			manyNames[i] = fmt.Sprintf("Справочник.Тест%d.МодульОбъекта", i)
		}
		for _, n := range []int{1, 3, 7, 16} {
			groups := splitByHash(manyNames, n)
			for shardIdx, group := range groups {
				for _, name := range group {
					if got := shardForID(name, n); got != shardIdx {
						t.Errorf("splitByHash put %q in shard %d, shardForID returns %d (n=%d)", name, shardIdx, got, n)
					}
				}
			}
		}
	})
}

func TestBuildShard(t *testing.T) {
	contentByName := map[string]string{
		"Справочник.Тест.МодульОбъекта": "Процедура Тест()\nКонецПроцедуры\n",
		"Документ.Док1.МодульОбъекта":   "Функция Вычислить()\n\tВозврат 42;\nКонецФункции\n",
	}
	names := []string{"Справочник.Тест.МодульОбъекта", "Документ.Док1.МодульОбъекта"}
	path := t.TempDir() + "/shard_0"

	idx, err := buildShard(path, names, func(name string) string { return contentByName[name] }, 0, 1, buildBSLMapping(), &atomic.Int64{})
	if err != nil {
		t.Fatalf("buildShard: %v", err)
	}
	defer idx.Close()

	docCount, err := idx.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	if docCount != 2 {
		t.Errorf("expected 2 docs, got %d", docCount)
	}

	mq := bleve.NewMatchQuery("Тест")
	mq.SetField("content")
	mq.Analyzer = "bsl"
	req := bleve.NewSearchRequest(mq)
	req.Size = 10
	result, err := idx.Search(req)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Total == 0 {
		t.Error("expected at least 1 search hit")
	}
}
