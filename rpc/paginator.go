package rpc

type Paginator[T any] struct {
	Items      []T
	Page       uint
	PageSize   uint
	TotalItems uint
	TotalPages uint
}

func NewPaginator[T any](items []T, page uint, pageSize uint, totalItems uint, totalPages uint) *Paginator[T] {
	return &Paginator[T]{
		Items:      items,
		Page:       page,
		PageSize:   pageSize,
		TotalItems: totalItems,
		TotalPages: totalPages,
	}
}

func (paginator *Paginator[T]) ToMap() map[string]any {
	return map[string]any{
		"items":       paginator.Items,
		"page":        paginator.Page,
		"page_size":   paginator.PageSize,
		"total_items": paginator.TotalItems,
		"total_pages": paginator.TotalPages,
	}
}

const DefaultPageSize = 20

// Paginate mirrors lbry-sdk's paginate_list (daemon.py:182): page defaults to 1
// and page_size to 20 independently, and both are at least 1.
func Paginate[T any](items []T, page uint, pageSize uint) *Paginator[T] {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	total := uint(len(items))
	offset := pageSize * (page - 1)
	sub := []T{}
	if offset <= total {
		sub = items[offset:min(offset+pageSize, total)]
	}
	return NewPaginator(sub, page, pageSize, total, (total+pageSize-1)/pageSize)
}
