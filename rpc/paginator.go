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
