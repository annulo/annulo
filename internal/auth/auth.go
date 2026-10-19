// Package auth 是服务端部署时的账号（docs/server-and-multi-project.md「账号」）：本机版只听 127.0.0.1，不用登录；
// 部署到服务器上，谁能打开、能做什么由这里管。
//
// 一套部署给一个互相信任的团队用：第一个人建管理员，别人靠管理员发的邀请链接加入。角色三种：
//
//	admin   管理员：全部，包括成员、设置、新建和升级项目
//	member  成员：用助手、跑任务、改数据、跑本机函数；不能动设置和成员
//	viewer  只读：看后台页面和数据；不能用助手、不能改数据，只能跑声明了 readonly 的本机函数
//
// 能用助手就等于能在服务器上执行命令，所以「成员」要谨慎给。
// 数据都在数据目录（容器里是 /data）：users.json（账号、邀请）、sessions.json（登录会话，只存 token 的哈希）。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/annulo/annulo/internal/i18n"
)

type Role string

const (
	Admin  Role = "admin"
	Member Role = "member"
	Viewer Role = "viewer"
)

func ValidRole(r Role) bool { return r == Admin || r == Member || r == Viewer }

// User 是一个账号。Hash 不出这个包（Users 返回的副本里是空的）。
type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      Role   `json:"role"`
	Hash      string `json:"hash,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Invite 是一个邀请链接：存 token 的哈希，链接里是 token 本身。
type Invite struct {
	ID        string `json:"id"`
	Hash      string `json:"hash,omitempty"`
	Role      Role   `json:"role"`
	CreatedBy string `json:"created_by"`
	ExpiresAt string `json:"expires_at"`
}

type session struct {
	User    string    `json:"user"`
	Expires time.Time `json:"expires"`
}

const (
	sessionTTL = 30 * 24 * time.Hour
	inviteTTL  = 7 * 24 * time.Hour
)

var (
	ErrBadLogin   = i18n.New("名字或密码不对", "Wrong name or password")
	ErrBadInvite  = i18n.New("邀请链接无效或已过期，请管理员重新发一个", "This invite link is invalid or has expired; ask an admin for a new one")
	ErrLastAdmin  = i18n.New("至少要留一个管理员", "There must be at least one admin")
	ErrNotFound   = i18n.New("没有这个账号", "No such account")
	ErrSetupDone  = i18n.New("已经有管理员了，请登录", "An admin already exists; please sign in")
	errShortPass  = i18n.New("密码至少 8 位", "The password must be at least 8 characters")
	errBadName    = i18n.New("名字不能为空，最多 40 个字，不能有空格", "The name can't be empty, must be at most 40 characters, and can't contain spaces")
	errNameTaken  = i18n.New("这个名字已经有人用了", "That name is already taken")
	errWrongOld   = i18n.New("原密码不对", "The current password is wrong")
	errBadRoleArg = i18n.New("角色只能是 admin、member、viewer", "The role must be admin, member or viewer")
)

type Store struct {
	dir string
	mu  sync.Mutex

	users    []User
	invites  []Invite
	sessions map[string]session // token 哈希 → 会话
}

// Open 读数据目录里的账号和会话（没有就是空的：界面先让第一个人建管理员）。
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, sessions: map[string]session{}}
	var data struct {
		Users   []User   `json:"users"`
		Invites []Invite `json:"invites"`
	}
	if err := readJSON(filepath.Join(dir, "users.json"), &data); err != nil {
		return nil, err
	}
	s.users, s.invites = data.Users, data.Invites
	if err := readJSON(filepath.Join(dir, "sessions.json"), &s.sessions); err != nil {
		return nil, err
	}
	return s, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// 调用方持有 s.mu
func (s *Store) saveUsers() error {
	return writeJSON(filepath.Join(s.dir, "users.json"), map[string]any{"users": s.users, "invites": s.invites})
}

func (s *Store) saveSessions() error {
	now := time.Now()
	for k, v := range s.sessions {
		if now.After(v.Expires) {
			delete(s.sessions, k)
		}
	}
	return writeJSON(filepath.Join(s.dir, "sessions.json"), s.sessions)
}

var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte(randomHex(8)), bcrypt.DefaultCost)
	return h
})

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func public(u User) User {
	u.Hash = ""
	return u
}

// Empty：还没有任何账号（第一次打开，界面让建管理员）。
func (s *Store) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users) == 0
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 40 || strings.ContainsAny(name, " \t\n") {
		return "", errBadName
	}
	return name, nil
}

func checkPassword(p string) error {
	if len(p) < 8 {
		return errShortPass
	}
	return nil
}

// 调用方持有 s.mu
func (s *Store) addUser(name, password string, role Role) (*User, error) {
	name, err := checkName(name)
	if err != nil {
		return nil, err
	}
	if err := checkPassword(password); err != nil {
		return nil, err
	}
	for _, u := range s.users {
		if strings.EqualFold(u.Name, name) {
			return nil, errNameTaken
		}
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := User{ID: "u" + randomHex(6), Name: name, Role: role, Hash: string(h), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	s.users = append(s.users, u)
	if err := s.saveUsers(); err != nil {
		s.users = s.users[:len(s.users)-1]
		return nil, err
	}
	return &u, nil
}

// 调用方持有 s.mu
func (s *Store) newSession(userID string) (string, error) {
	tok := randomHex(32)
	s.sessions[hashToken(tok)] = session{User: userID, Expires: time.Now().Add(sessionTTL)}
	return tok, s.saveSessions()
}

// Setup 建第一个账号（管理员），返回登录会话的 token。已经有账号就拒绝。
func (s *Store) Setup(name, password string) (User, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) > 0 {
		return User{}, "", ErrSetupDone
	}
	u, err := s.addUser(name, password, Admin)
	if err != nil {
		return User{}, "", err
	}
	tok, err := s.newSession(u.ID)
	return public(*u), tok, err
}

// Login 用名字（不分大小写）和密码登录，返回会话 token。
func (s *Store) Login(name, password string) (User, string, error) {
	s.mu.Lock()
	var hit *User
	for i := range s.users {
		if strings.EqualFold(s.users[i].Name, strings.TrimSpace(name)) {
			hit = &s.users[i]
		}
	}
	var hash string
	if hit != nil {
		hash = hit.Hash
	}
	s.mu.Unlock()
	if hit == nil {
		// 没有这个人也算一遍哈希：响应时间不暴露名字存不存在
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		time.Sleep(300 * time.Millisecond)
		return User{}, "", ErrBadLogin
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		time.Sleep(300 * time.Millisecond)
		return User{}, "", ErrBadLogin
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.newSession(hit.ID)
	return public(*hit), tok, err
}

// UserBySession 是会话 token 对应的账号；过期、账号删了返回 nil。用着的会话自动续期。
func (s *Store) UserBySession(tok string) *User {
	if tok == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := hashToken(tok)
	se, ok := s.sessions[k]
	if !ok || time.Now().After(se.Expires) {
		return nil
	}
	for _, u := range s.users {
		if u.ID == se.User {
			if time.Until(se.Expires) < sessionTTL-24*time.Hour { // 一天续一次，不每个请求都写盘
				se.Expires = time.Now().Add(sessionTTL)
				s.sessions[k] = se
				s.saveSessions()
			}
			p := public(u)
			return &p
		}
	}
	return nil
}

func (s *Store) Logout(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hashToken(tok))
	s.saveSessions()
}

// CreateInvite 发一个邀请（7 天有效），返回链接里用的 token。
func (s *Store) CreateInvite(role Role, by string) (string, error) {
	if !ValidRole(role) {
		return "", errBadRoleArg
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := randomHex(20)
	s.invites = append(s.invites, Invite{ID: "i" + randomHex(6), Hash: hashToken(tok), Role: role, CreatedBy: by, ExpiresAt: time.Now().Add(inviteTTL).UTC().Format(time.RFC3339)})
	return tok, s.saveUsers()
}

// 调用方持有 s.mu
func (s *Store) findInvite(tok string) int {
	h := hashToken(tok)
	now := time.Now()
	for i, in := range s.invites {
		exp, _ := time.Parse(time.RFC3339, in.ExpiresAt)
		if in.Hash == h && now.Before(exp) {
			return i
		}
	}
	return -1
}

// InviteRole 是邀请给的角色；无效、过期返回 false。
func (s *Store) InviteRole(tok string) (Role, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.findInvite(tok); i >= 0 {
		return s.invites[i].Role, true
	}
	return "", false
}

// AcceptInvite 用邀请建账号并登录。邀请用一次就作废。
func (s *Store) AcceptInvite(tok, name, password string) (User, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findInvite(tok)
	if i < 0 {
		return User{}, "", ErrBadInvite
	}
	role := s.invites[i].Role
	u, err := s.addUser(name, password, role)
	if err != nil {
		return User{}, "", err
	}
	s.invites = slices.Delete(s.invites, i, i+1)
	if err := s.saveUsers(); err != nil {
		return User{}, "", err
	}
	sess, err := s.newSession(u.ID)
	return public(*u), sess, err
}

// Users 是全部账号（不带密码哈希），Invites 是还没用、没过期的邀请。
func (s *Store) Users() ([]User, []Invite) {
	s.mu.Lock()
	defer s.mu.Unlock()
	users := make([]User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, public(u))
	}
	invites := []Invite{}
	now := time.Now()
	for _, in := range s.invites {
		if exp, _ := time.Parse(time.RFC3339, in.ExpiresAt); now.Before(exp) {
			in.Hash = ""
			invites = append(invites, in)
		}
	}
	return users, invites
}

// 调用方持有 s.mu
func (s *Store) admins() int {
	n := 0
	for _, u := range s.users {
		if u.Role == Admin {
			n++
		}
	}
	return n
}

// SetRole 改角色；不能把最后一个管理员降下来。
func (s *Store) SetRole(id string, role Role) error {
	if !ValidRole(role) {
		return errBadRoleArg
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].ID == id {
			if s.users[i].Role == Admin && role != Admin && s.admins() == 1 {
				return ErrLastAdmin
			}
			s.users[i].Role = role
			return s.saveUsers()
		}
	}
	return ErrNotFound
}

// Delete 删账号，同时让它的会话失效；不能删最后一个管理员。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.users {
		if u.ID == id {
			if u.Role == Admin && s.admins() == 1 {
				return ErrLastAdmin
			}
			s.users = slices.Delete(s.users, i, i+1)
			for k, se := range s.sessions {
				if se.User == id {
					delete(s.sessions, k)
				}
			}
			s.saveSessions()
			return s.saveUsers()
		}
	}
	return ErrNotFound
}

// DeleteInvite 撤回一个还没用的邀请。
func (s *Store) DeleteInvite(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, in := range s.invites {
		if in.ID == id {
			s.invites = slices.Delete(s.invites, i, i+1)
			return s.saveUsers()
		}
	}
	return ErrNotFound
}

// SetPassword 改自己的密码：要原密码。
func (s *Store) SetPassword(id, old, next string) error {
	if err := checkPassword(next); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].ID == id {
			if bcrypt.CompareHashAndPassword([]byte(s.users[i].Hash), []byte(old)) != nil {
				return errWrongOld
			}
			h, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			s.users[i].Hash = string(h)
			return s.saveUsers()
		}
	}
	return ErrNotFound
}

// CLIToken 是容器里 annulo 命令行（助手跑的 annulo run …）调服务用的令牌：存在数据目录的 cli-token（0600），
// 没有就生成。能读这个文件的就是能在这台服务器上执行命令的，所以它按管理员算。
func CLIToken(dir string) string {
	path := filepath.Join(dir, "cli-token")
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b))
	}
	tok := randomHex(32)
	os.WriteFile(path, []byte(tok), 0o600)
	return tok
}
