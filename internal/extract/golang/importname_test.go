package golang

import "testing"

func TestGoImportName(t *testing.T) {
	for path, want := range map[string]string{
		"fmt":                                   "fmt",
		"net/http":                              "http",
		"gopkg.in/yaml.v3":                      "yaml",
		"github.com/go-redis/redis/v9":          "redis",
		"github.com/jackc/pgx/v5":               "pgx",
		"github.com/olivere/elastic/v7":         "elastic",
		"github.com/mattn/go-sqlite3":           "sqlite3",
		"github.com/hashicorp/go-retryablehttp": "retryablehttp",
		"github.com/stripe/stripe-go":           "stripe",
		"github.com/x/v2":                       "x",
		"example.com/foo/v1":                    "v1",
		"example.com/foo.v1":                    "foo.v1",
	} {
		if got := goImportName(path); got != want {
			t.Errorf("goImportName(%q) = %q, want %q", path, got, want)
		}
	}
}
