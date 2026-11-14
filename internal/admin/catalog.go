package admin

import "github.com/sdrahn/custos/internal/category"

// Categories reads the category catalog (CONTRACT.md §6a).
func (a *Admin) Categories() (map[string]category.Category, error) { return category.Load(a.Root) }

// CheckCategory refuses a category the catalog does not have.
func (a *Admin) CheckCategory(cat string) error { return category.Check(a.Root, cat) }
