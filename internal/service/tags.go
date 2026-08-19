package service

import (
	"context"
	"fmt"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Tags.
//
// A tag is created by using it. Somebody typing `#incident` into quick-add gets
// the tag; there is no separate step to define one first, because a labelling
// system that has to be set up before it can be used does not get used. The
// management screen exists to rename, recolour and tidy afterwards.

// Tags lists every tag with how many entries carry it.
func (s *Service) Tags(ctx context.Context) ([]domain.Tag, error) {
	if _, err := auth.MustUser(ctx); err != nil {
		return nil, err
	}
	return s.db.ListTags(ctx)
}

// UpdateTag renames or recolours a tag.
//
// Renaming to a name that already exists is refused rather than merging the
// two: merging is a different, destructive operation and it should not happen
// by way of a typo in a rename box.
func (s *Service) UpdateTag(ctx context.Context, tag domain.Tag) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "tag"}); err != nil {
		return notFoundFor(err)
	}
	if err := tag.Validate(); err != nil {
		return err
	}

	name := domain.NormaliseTag(tag.Name)
	existing, err := s.db.ListTags(ctx)
	if err != nil {
		return err
	}
	for _, other := range existing {
		if other.Name == name && other.ID != tag.ID {
			return fmt.Errorf("%w: a tag called %q already exists", ErrConflict, name)
		}
	}

	tag.Name = name
	if err := s.db.UpdateTag(ctx, tag); err != nil {
		return err
	}
	// A rename changes the words entries are findable by, so the index has to
	// follow it. Cheap relative to how rarely tags are renamed.
	if _, err := s.db.ReindexSearch(ctx); err != nil {
		return err
	}
	return s.recordAudit(ctx, "tag.update", "tag", tag.ID, map[string]any{
		"name": name,
	})
}

// DeleteTag removes a tag from every entry that carries it.
//
// Safe in a way deleting a customer is not: nothing is invoiced against a tag,
// so removing one loses a label rather than orphaning history. The audit record
// keeps the name, so a tag deleted by mistake can at least be identified.
func (s *Service) DeleteTag(ctx context.Context, id int64) error {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "tag"}); err != nil {
		return notFoundFor(err)
	}

	tags, err := s.db.ListTags(ctx)
	if err != nil {
		return err
	}
	var removed domain.Tag
	for _, tag := range tags {
		if tag.ID == id {
			removed = tag
			break
		}
	}

	if err := s.db.DeleteTag(ctx, id); err != nil {
		return err
	}
	if _, err := s.db.ReindexSearch(ctx); err != nil {
		return err
	}
	return s.recordAudit(ctx, "tag.delete", "tag", id, map[string]any{
		"name":    removed.Name,
		"entries": removed.EntryCount,
	})
}

// ReindexSearch rebuilds the full-text index.
//
// Offered to an administrator rather than run at startup: on a long history it
// is expensive, and a stale index degrades search rather than breaking the
// application. It is the repair after a restore, which writes entries without
// going through the normal path.
func (s *Service) ReindexSearch(ctx context.Context) (int, error) {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "settings"}); err != nil {
		return 0, notFoundFor(err)
	}
	count, err := s.db.ReindexSearch(ctx)
	if err != nil {
		return 0, err
	}
	if err := s.recordAudit(ctx, "search.reindex", "settings", 0, map[string]any{
		"entries": count,
	}); err != nil {
		return count, err
	}
	return count, nil
}
