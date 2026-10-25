package postgres_test

import (
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// TestPostgresRepository_Conformance runs the identical shared repository conformance
// suite against PostgreSQL durable storage in an isolated schema.
func TestPostgresRepository_Conformance(t *testing.T) {
	storage.RunRepositoryConformanceTests(t, func(t *testing.T) storage.Repository {
		pool, _ := setupTestPool(t)
		return postgres.NewRepository(pool)
	})
}
