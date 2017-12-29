package models

import umtypes "git.containerum.net/ch/json-types/user-manager"

type UserRole int

type User struct {
	ID            string           `db:"id"`
	Login         string           `db:"login"`
	PasswordHash  string           `db:"password_hash"` // base64
	Salt          string           `db:"salt"`          // base64
	Role          umtypes.UserRole `db:"role"`
	IsActive      bool             `db:"is_active"`
	IsDeleted     bool             `db:"is_deleted"`
	IsInBlacklist bool             `db:"is_in_blacklist"`
}

const userQueryColumns = "id, login, password_hash, salt, role, is_active, is_deleted, is_in_blacklist"

func (db *DB) GetUserByLogin(login string) (*User, error) {
	db.log.Debugln("Get user by login", login)
	var user User
	rows, err := db.qLog.Queryx("SELECT "+userQueryColumns+" FROM users WHERE login = $1 AND NOT is_deleted", login)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	err = rows.StructScan(&user)
	return &user, err
}

func (db *DB) GetUserByID(id string) (*User, error) {
	db.log.Debugln("Get user by id", id)
	var user User
	rows, err := db.qLog.Queryx("SELECT "+userQueryColumns+" FROM users WHERE id = $1 AND NOT is_deleted", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	err = rows.StructScan(&user)
	return &user, err
}

func (db *DB) CreateUser(user *User) error {
	db.log.Debugln("Create user", user.Login)
	rows, err := db.qLog.Queryx("INSERT INTO users (login, password_hash, salt, role) "+
		"VALUES ($1, $2, $3, $4) RETURNING id",
		user.Login, user.PasswordHash, user.Salt, user.Role)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	err = rows.Scan(&user.ID)
	return err
}

func (db *DB) UpdateUser(user *User) error {
	db.log.Debugln("Update user", user.Login)
	_, err := db.eLog.Exec("UPDATE users SET "+
		"login = $2, password_hash = $3, salt = $4, role = $5, is_active = $6, is_deleted = $7 WHERE id = $1",
		user.ID, user.Login, user.PasswordHash, user.Salt, user.Role, user.IsActive, user.IsDeleted)
	return err
}

func (db *DB) GetBlacklistedUsers(perPage, page int) ([]User, error) {
	db.log.Debugln("Get blacklisted users")
	resp := make([]User, 0)
	rows, err := db.qLog.Queryx("SELECT "+userQueryColumns+" FROM users WHERE is_in_blacklist LIMIT $1 OFFSET $2",
		perPage, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user User
		err := rows.StructScan(&user)
		if err != nil {
			return nil, err
		}
		resp = append(resp, user)
	}
	return resp, rows.Err()
}

func (db *DB) BlacklistUser(user *User) error {
	db.log.Debugln("Blacklisting user", user.Login)
	_, err := db.eLog.Exec("UPDATE users SET is_in_blacklist = TRUE WHERE id = $1", user.ID)
	if err != nil {
		return err
	}
	_, err = db.eLog.Exec("UPDATE profiles SET blacklist_at = NOW() WHERE user_id = $1", user.ID)
	if err != nil {
		return err
	}
	user.IsInBlacklist = true
	return nil
}
