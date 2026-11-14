package rpc

import "testing"

// Mirrors lbry-sdk's paginate_list (daemon.py:182): page and page_size default
// independently to 1 and 20.
func TestPaginate(t *testing.T) {
	items := []string{"a", "b", "c"}
	for _, tc := range []struct {
		name             string
		page, size       uint
		want             []string
		wantPage, wantSz uint
		wantPages        uint
	}{
		{"defaults", 0, 0, []string{"a", "b", "c"}, 1, 20, 1},
		{"page_size only", 0, 1, []string{"a"}, 1, 1, 3},
		{"page only", 2, 0, []string{}, 2, 20, 1},
		{"both", 2, 2, []string{"c"}, 2, 2, 2},
		{"past the end", 9, 2, []string{}, 9, 2, 2},
	} {
		got := Paginate(items, tc.page, tc.size)
		if len(got.Items) != len(tc.want) || got.Page != tc.wantPage || got.PageSize != tc.wantSz || got.TotalItems != 3 || got.TotalPages != tc.wantPages {
			t.Errorf("%s: got %+v", tc.name, got)
			continue
		}
		for i := range tc.want {
			if got.Items[i] != tc.want[i] {
				t.Errorf("%s: item %d is %q, want %q", tc.name, i, got.Items[i], tc.want[i])
			}
		}
	}
	if got := Paginate([]string{}, 0, 0); got.TotalPages != 0 || got.Page != 1 {
		t.Errorf("empty list: %+v", got)
	}
}
