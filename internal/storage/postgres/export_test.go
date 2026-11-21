package postgres

// MapPgError exposes mapPgError to the external tests, so they can feed it real PostgreSQL errors
// (taken from real constraint violations) instead of fabricated ones.
var MapPgError = mapPgError
