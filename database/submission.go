package database

import (
	"database/sql"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Submission is db table
type Submission struct {
	ID               int32 `gorm:"primaryKey"`
	SubmissionTime   time.Time
	ProblemName      string
	Problem          Problem `gorm:"foreignKey:ProblemName"`
	Lang             string
	Status           string
	PrevStatus       string
	Hacked           bool
	Source           string
	TestCasesVersion string
	MaxTime          int32
	MaxMemory        int64
	CompileError     []byte
	UserName         sql.NullString
	User             User `gorm:"foreignKey:UserName"`
	JudgedTime       time.Time
}

// SubmissionOverview is smart select table
type SubmissionOverView struct {
	ID               int32
	SubmissionTime   time.Time
	ProblemName      string
	Problem          Problem
	Lang             string
	Status           string
	TestCasesVersion string
	MaxTime          int32
	MaxMemory        int64
	UserName         sql.NullString
	User             User
}

func ToSubmissionOverView(s Submission) SubmissionOverView {
	return SubmissionOverView{
		ID:               s.ID,
		SubmissionTime:   s.SubmissionTime,
		ProblemName:      s.ProblemName,
		Problem:          s.Problem,
		Lang:             s.Lang,
		Status:           s.Status,
		TestCasesVersion: s.TestCasesVersion,
		MaxTime:          s.MaxTime,
		MaxMemory:        s.MaxMemory,
		UserName:         s.UserName,
		User:             s.User,
	}
}

type SubmissionOrder int

const (
	ID_DESC SubmissionOrder = iota
	MAX_TIME_ASC
)

// SubmissionTestcaseResult is db table
type SubmissionTestcaseResult struct {
	Submission int32  `gorm:"primaryKey"` // TODO: should be foreign key
	Testcase   string `gorm:"primaryKey"`
	Status     string
	Time       int32
	Memory     int64
	Stderr     []byte
	CheckerOut []byte
}

func FetchSubmission(db *gorm.DB, id int32) (*Submission, error) {
	sub := Submission{
		ID: id,
	}
	if err := db.
		Preload("User").
		Preload("Problem").
		Take(&sub).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	return &sub, nil
}

// save submission and return id
func SaveSubmission(db *gorm.DB, submission Submission) (int32, error) {
	if submission.ID != 0 {
		return 0, errors.New("must not specify submission id")
	}
	if err := db.Save(&submission).Error; err != nil {
		return 0, err
	}

	return submission.ID, nil
}

func UpdateSubmission(db *gorm.DB, submission Submission) error {
	if submission.ID == 0 {
		return errors.New("must specify submission id")
	}
	if err := db.Save(&submission).Error; err != nil {
		return err
	}

	return nil
}

func ClearTestcaseResult(db *gorm.DB, subID int32) error {
	if err := db.Where("submission = ?", subID).Delete(&SubmissionTestcaseResult{}).Error; err != nil {
		return err
	}
	return nil
}

func SaveTestcaseResult(db *gorm.DB, result SubmissionTestcaseResult) error {
	if err := db.Save(&result).Error; err != nil {
		return err
	}

	return nil
}

func FetchTestcaseResults(db *gorm.DB, id int32) ([]SubmissionTestcaseResult, error) {
	var cases []SubmissionTestcaseResult
	if err := db.Where("submission = ?", id).Find(&cases).Error; err != nil {
		return nil, err
	}

	// TODO: to DB query
	sort.Slice(cases, func(i, j int) bool {
		return cases[i].Testcase < cases[j].Testcase
	})

	return cases, nil
}

func FetchSubmissionList(db *gorm.DB, problem, status, lang, user string, order []SubmissionOrder, offset, limit int) ([]SubmissionOverView, int64, error) {
	filter := &Submission{
		ProblemName: problem,
		Status:      status,
		Lang:        lang,
		UserName:    sql.NullString{String: user, Valid: (user != "")},
	}

	count := int64(0)
	if err := db.Model(&Submission{}).Where(filter).Count(&count).Error; err != nil {
		return nil, 0, errors.New("count query failed")
	}

	query := db.Model(&Submission{}).Where(filter).Limit(limit).Offset(offset).
		Preload("User").
		Preload("Problem")
	for _, o := range order {
		switch o {
		case ID_DESC:
			query.Order("id desc")
		case MAX_TIME_ASC:
			query.Order("max_time asc")
		}
	}

	var submissions = make([]SubmissionOverView, 0)
	if err := query.Find(&submissions).Error; err != nil {
		return nil, 0, err
	}

	return submissions, count, nil
}

const LOCK_TIME = time.Minute

type SubmissionLock struct {
	ID         int32      `gorm:"primaryKey"`
	Submission Submission `gorm:"foreignKey:ID"`
	Name       string
	Ping       time.Time
}

func TryLockSubmission(db *gorm.DB, id int32, name string) (bool, error) {
	now := time.Now()

	succeeded := false
	if err := db.Transaction(func(tx *gorm.DB) error {
		lock := SubmissionLock{}
		if err := tx.Where(SubmissionLock{
			ID: id,
		}).Attrs(SubmissionLock{
			ID:   id,
			Name: name,
			Ping: time.Time{},
		}).Clauses(clause.Locking{Strength: "UPDATE"}).FirstOrCreate(&lock).Error; err != nil {
			return err
		}

		if lock.Name != name && now.Before(lock.Ping.Add(LOCK_TIME)) {
			// already locked by another judge
			return nil
		}

		lock.Name = name
		lock.Ping = now
		succeeded = true

		return tx.Save(lock).Error
	}); err != nil {
		return false, err
	}

	return succeeded, nil
}

func UnlockSubmission(db *gorm.DB, id int32, name string) error {
	if ok, err := TryLockSubmission(db, id, name); err != nil {
		return err
	} else if !ok {
		return errors.New("failed to lock")
	}

	return db.Delete(&SubmissionLock{
		ID: id,
	}).Error
}
