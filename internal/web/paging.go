package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const perPage = 25

// paging is the ?page and ?q of a list page. Filters are the other query
// parameters that the search form and the pager links carry, like ?kind.
type paging struct {
	Page    int
	Query   string
	More    bool
	Dated   bool // newest first, the pager says Newer and Older
	Per     int  // rows per page, perPage when zero
	path    string
	filters url.Values
}

func newPaging(r *http.Request) paging {
	q := r.URL.Query()
	p := paging{Query: strings.TrimSpace(q.Get("q")), path: r.URL.Path, filters: url.Values{}}
	p.Page, _ = strconv.Atoi(q.Get("page"))
	p.Page = max(p.Page, 1)
	return p
}

// Add keeps one more value of a filter that can repeat.
func (p *paging) Add(key, value string) { p.filters.Add(key, value) }

func (p *paging) Keep(key, value string) {
	if value != "" {
		p.filters.Set(key, value)
	}
}

func (p paging) per() int {
	if p.Per > 0 {
		return p.Per
	}
	return perPage
}

func (p paging) Offset() int { return (p.Page - 1) * p.per() }

// Limit asks the database for one row more than a page, cut tells from it whether there is a next page.
func (p paging) Limit() int { return p.per() + 1 }

func (p paging) Path() string { return p.path }

func (p paging) Filters() map[string]string {
	out := make(map[string]string, len(p.filters))
	for k := range p.filters {
		out[k] = p.filters.Get(k)
	}
	return out
}

func (p paging) Shown() bool  { return p.Page > 1 || p.More }
func (p paging) Self() string { return p.link(p.filters, p.Page) }
func (p paging) Prev() string { return p.link(p.filters, p.Page-1) }
func (p paging) Next() string { return p.link(p.filters, p.Page+1) }

// With links to the first page with only this filter and the search, for the filter pills. An empty key clears the filters.
func (p paging) With(key, value string) string {
	v := url.Values{}
	if key != "" && value != "" {
		v.Set(key, value)
	}
	return p.link(v, 1)
}

func (p paging) link(filters url.Values, page int) string {
	v := url.Values{}
	for k, vs := range filters {
		v[k] = vs
	}
	if p.Query != "" {
		v.Set("q", p.Query)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return p.path
	}
	return p.path + "?" + v.Encode()
}

// Matches filters in memory, for lists that do not come from the database.
func (p paging) Matches(fields ...string) bool {
	if p.Query == "" {
		return true
	}
	q := strings.ToLower(p.Query)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// cut trims a result fetched with Limit to one page.
func cut[T any](p *paging, list []T) []T {
	if p.More = len(list) > p.per(); p.More {
		list = list[:p.per()]
	}
	return list
}

// window returns the current page of a list filtered in memory.
func window[T any](p *paging, list []T) []T {
	from := min(p.Offset(), len(list))
	to := min(from+p.per(), len(list))
	p.More = to < len(list)
	return list[from:to]
}
