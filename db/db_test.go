package db_test

import (
	"app/db"
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

var testDbName = "test.db"

func TestCreateDB(t *testing.T) {
	assert := assert.New(t)

	_ = os.Remove(testDbName)

	var testDB *sql.DB

	assert.NotPanics(func() {
		testDB = db.CreateDb(testDbName)
	})

	_, err := testDB.Query("select * from custom_chars")
	assert.NoError(err)

	_ = os.Remove(testDbName)
}
