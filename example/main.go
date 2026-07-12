package main

import (
	"fmt"
	"os"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/example/generated"
)

func main() {
	dir, err := os.MkdirTemp("", "sqlitex-example-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	db, err := sqlitex.Open(sqlitex.Config{Dir: dir})
	if err != nil {
		panic(err)
	}
	defer db.Close()

	store := generated.NewUserStore(db)

	fmt.Println("=== SQLiteX CRUD Demo ===\n")

	// Create
	fmt.Println("[Create]")
	store.Create(&generated.User{Id: 1, Name: "Alice", Email: "alice@test.com", Active: true})
	store.Create(&generated.User{Id: 2, Name: "Bob", Email: "bob@test.com", Active: false})
	store.Create(&generated.User{Id: 3, Name: "Charlie", Email: "charlie@test.com", Active: true})
	fmt.Println("  3 rows inserted\n")

	// Get
	fmt.Println("[Get] id=1")
	u, _ := store.Get(1)
	fmt.Printf("  %+v\n\n", u)

	// Update
	fmt.Println("[Update] id=1 name -> \"Alice Updated\"")
	u.Name = "Alice Updated"
	store.Update(u)
	u, _ = store.Get(1)
	fmt.Printf("  %+v\n\n", u)

	// Delete
	fmt.Println("[Delete] id=2")
	store.Delete(2)
	u, _ = store.Get(2)
	fmt.Printf("  id=2 exists: %v\n\n", u != nil)

	// Query
	fmt.Println("[Query] all rows")
	results, _ := generated.NewUserQuery(db).Exec()
	for _, r := range results {
		fmt.Printf("  id=%d, name=%s, email=%s, active=%v\n", r.Id, r.Name, r.Email, r.Active)
	}
	fmt.Println()

	// First
	fmt.Println("[First]")
	first, _ := generated.NewUserQuery(db).First()
	fmt.Printf("  %+v\n\n", first)

	// Count
	fmt.Println("[Count]")
	count, _ := generated.NewUserQuery(db).Count()
	fmt.Printf("  total: %d\n\n", count)

	fmt.Println("=== Done ===")
}
