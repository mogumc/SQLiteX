package main

import (
	"fmt"
	"os"

	"github.com/mogumc/sqlitex"
	"github.com/mogumc/sqlitex/example/demo"
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

	userStore := demo.NewUserStore(db)

	fmt.Println("=== SQLiteX Demo: User (CRUD + Query) ===")

	// Create
	fmt.Println("[Create]")
	userStore.Create(&demo.User{Id: 1, Name: "Alice", Email: "alice@test.com", Active: true})
	userStore.Create(&demo.User{Id: 2, Name: "Bob", Email: "bob@test.com", Active: false})
	fmt.Println("  2 rows inserted")

	// Get
	fmt.Println("[Get] id=1")
	u, _ := userStore.Get(1)
	fmt.Printf("  %+v\n\n", u)

	// Update
	fmt.Println("[Update] id=1 name -> \"Alice Updated\"")
	u.Name = "Alice Updated"
	userStore.Update(u)
	u, _ = userStore.Get(1)
	fmt.Printf("  %+v\n\n", u)

	// Delete
	fmt.Println("[Delete] id=2")
	userStore.Delete(2)
	u, _ = userStore.Get(2)
	fmt.Printf("  id=2 exists: %v\n\n", u != nil)

	// Query (索引: 唯一索引 email)
	fmt.Println("[Query] WHERE email='alice@test.com' (二级索引)")
	users, _ := demo.NewUserQuery(db).WhereEmail("=", "alice@test.com").Exec()
	for _, r := range users {
		fmt.Printf("  id=%d, name=%s, email=%s\n", r.Id, r.Name, r.Email)
	}
	fmt.Println()

	// Query (LIKE)
	fmt.Println("[Query] WHERE name LIKE 'Ali'")
	users, _ = demo.NewUserQuery(db).WhereName("LIKE", "Ali").Exec()
	for _, r := range users {
		fmt.Printf("  id=%d, name=%s\n", r.Id, r.Name)
	}
	fmt.Println()

	// Count
	count, _ := demo.NewUserQuery(db).Count()
	fmt.Printf("[Count] total: %d\n\n", count)

	sessionStore := demo.NewSessionStore(db)

	fmt.Println("=== SQLiteX Demo: Session (TTL) ===")
	err = sessionStore.Create(&demo.Session{Id: 1, Token: "tok-1", UserId: "u1", Active: true})
	if err != nil {
		panic(err)
	}
	fmt.Println("[Create] session with TTL=1s")
	s, _ := sessionStore.Get(1)
	fmt.Printf("  before expiry: %+v\n", s)

	fmt.Println("=== Done ===")
}
