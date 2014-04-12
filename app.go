package main

import (
	"errors"
	"fmt"
	"regexp"

	ct "github.com/flynn/flynn-controller/types"
	"github.com/flynn/flynn-controller/utils"
	"github.com/flynn/go-sql"
)

type AppRepo struct {
	db *DB
}

func NewAppRepo(db *DB) *AppRepo {
	return &AppRepo{db}
}

var appNamePattern = regexp.MustCompile(`^[a-z\d]+(-[a-z\d]+)*$`)

func (r *AppRepo) Add(data interface{}) error {
	app := data.(*ct.App)
	// TODO: actually validate
	if app.Name == "" {
		return errors.New("controller: app name must not be blank")
	}
	if len(app.Name) > 30 || !appNamePattern.MatchString(app.Name) {
		return errors.New("controller: invalid app name")
	}
	if app.ID == "" {
		app.ID = utils.UUID()
	}
	err := r.db.QueryRow("INSERT INTO apps (app_id, name, protected) VALUES ($1, $2, $3) RETURNING created_at, updated_at", app.ID, app.Name, app.Protected).Scan(&app.CreatedAt, &app.UpdatedAt)
	app.ID = cleanUUID(app.ID)
	return err
}

var ErrNotFound = errors.New("controller: resource not found")

func scanApp(s Scanner) (*ct.App, error) {
	app := &ct.App{}
	err := s.Scan(&app.ID, &app.Name, &app.Protected, &app.CreatedAt, &app.UpdatedAt)
	if err == sql.ErrNoRows {
		err = ErrNotFound
	}
	app.ID = cleanUUID(app.ID)
	return app, err
}

var idPattern = regexp.MustCompile(`^[a-f0-9]{8}-?([a-f0-9]{4}-?){3}[a-f0-9]{12}$`)

type rowQueryer interface {
	QueryRow(query string, args ...interface{}) Scanner
}

func selectApp(db rowQueryer, id string, update bool) (*ct.App, error) {
	var row Scanner
	query := "SELECT app_id, name, protected, created_at, updated_at FROM apps WHERE deleted_at IS NULL AND "
	var suffix string
	if update {
		suffix = " FOR UPDATE"
	}
	if idPattern.MatchString(id) {
		row = db.QueryRow(query+"(app_id = $1 OR name = $2) LIMIT 1"+suffix, id, id)
	} else {
		row = db.QueryRow(query+"name = $1"+suffix, id)
	}
	return scanApp(row)
}

func (r *AppRepo) Get(id string) (interface{}, error) {
	return selectApp(r.db, id, false)
}

func (r *AppRepo) Update(id string, data map[string]interface{}) (interface{}, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	app, err := selectApp(tx, id, true)
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	for k, v := range data {
		switch k {
		case "protected":
			protected, ok := v.(bool)
			if !ok {
				tx.Rollback()
				return nil, fmt.Errorf("controller: expected bool, got %T", v)
			}
			if app.Protected != protected {
				if _, err := tx.Exec("UPDATE apps SET protected = $2 WHERE app_id = $1", app.ID, protected); err != nil {
					tx.Rollback()
					return nil, err
				}
				app.Protected = protected
			}
		}
	}

	return app, tx.Commit()
}

func (r *AppRepo) List() (interface{}, error) {
	rows, err := r.db.Query("SELECT app_id, name, protected, created_at, updated_at FROM apps WHERE deleted_at IS NULL ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	var apps []*ct.App
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (r *AppRepo) SetRelease(appID string, releaseID string) error {
	return r.db.Exec("UPDATE apps SET release_id = $2, updated_at = now() WHERE app_id = $1", appID, releaseID)
}

func (r *AppRepo) GetRelease(id string) (*ct.Release, error) {
	row := r.db.QueryRow("SELECT r.release_id, r.artifact_id, r.data, r.created_at FROM apps a JOIN releases r USING (release_id) WHERE a.app_id = $1", id)
	return scanRelease(row)
}
