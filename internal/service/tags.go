package service

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Tags.
//
// A tag is created by using it. Somebody typing `#incident` into quick-add gets
// the tag; there is no separate step to define one first, because a labelling
// system that has to be set up before it can be used does not get used. The
// management screen exists to rename, recolour and tidy afterwards.

// Tags lists the tags on work the actor can see, with how many of those entries
// carry each one.
//
// A tag is created by being used, so the vocabulary is a description of the work
// it labels - which is why the whole of it is not everybody's to read. The full
// list told a member which labels exist on projects they are not on, and the
// full count told them how much work carries each; "globex-escalation" with
// forty entries behind it is a sentence about somebody else's engagement. It is
// scoped the same way the entries listing is, so the vocabulary a filter offers
// is the vocabulary that filter can match, and a tag's count agrees with the
// rows behind it.
//
// A client gets nothing rather than a narrowed list, because there is no
// narrowed list to give: the projection strips tags from their entries on
// purpose - a label is internal shorthand and nothing stops one being a candid
// opinion - so by the time a client sees an entry it carries none.
//
// It stays a listing of every tag the actor can see rather than only the ones
// they wrote, because choosing from what the team already uses is the whole
// point of a shared vocabulary: a labelling system where everybody invents
// their own spelling of "incident" is not one.
func (s *Service) Tags(ctx context.Context) ([]domain.Tag, error) {
	if _, err := auth.MustUser(ctx); err != nil {
		return nil, err
	}
	if actingAsClient(ctx) {
		return nil, nil
	}
	scope, err := s.effectiveScope(ctx)
	if err != nil {
		return nil, err
	}
	return s.db.ListTags(ctx, scope)
}

// TagsForAdmin lists the whole vocabulary, for the screen that renames and
// deletes them.
//
// Every control on that screen is administration, so opening it is too. It was
// authorised as a plain read, which made it a page of six forms a member could
// not submit - and a page of another customer's labels for a client.
//
// Unscoped, which is the point of it being administrative: tidying a vocabulary
// means seeing all of it, including the tag used once on a project the
// administrator does not happen to be a member of, and the true count is what
// makes "delete this one" a safe decision.
func (s *Service) TagsForAdmin(ctx context.Context) ([]domain.Tag, error) {
	if err := s.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "tag"}); err != nil {
		return nil, notFoundFor(err)
	}
	return s.db.ListTags(ctx, store.UnrestrictedScope())
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
	// The whole vocabulary: a rename collides with every existing name, not
	// only with the ones this administrator can see work under.
	existing, err := s.db.ListTags(ctx, store.UnrestrictedScope())
	if err != nil {
		return err
	}
	for _, other := range existing {
		if other.Name == name && other.ID != tag.ID {
			return fmt.Errorf("%w: a tag called %q already exists", ErrConflict, name)
		}
	}

	tag.Name = name
	return s.mutate(ctx, "tag.update", "tag", map[string]any{
		"name": name,
	}, func(tx *sql.Tx) (int64, error) {
		if err := store.UpdateTagTx(ctx, tx, tag); err != nil {
			return 0, err
		}
		// A rename changes the words entries are findable by, so the index has
		// to follow it - in the same transaction, because the new name is not
		// visible to another connection until this one commits, and a rebuild
		// beside it would index the old name.
		_, err := store.ReindexSearchTx(ctx, tx)
		return tag.ID, err
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

	// The whole vocabulary, so a tag can be deleted by id whatever work carries
	// it: this is the administrative listing's counterpart.
	tags, err := s.db.ListTags(ctx, store.UnrestrictedScope())
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

	return s.mutate(ctx, "tag.delete", "tag", map[string]any{
		"name":    removed.Name,
		"entries": removed.EntryCount,
	}, func(tx *sql.Tx) (int64, error) {
		if err := store.DeleteTagTx(ctx, tx, id); err != nil {
			return 0, err
		}
		_, err := store.ReindexSearchTx(ctx, tx)
		return id, err
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
	var count int
	if err := s.mutate(ctx, "search.reindex", "settings", nil, func(tx *sql.Tx) (int64, error) {
		var txErr error
		count, txErr = store.ReindexSearchTx(ctx, tx)
		return 0, txErr
	}); err != nil {
		return 0, err
	}
	return count, nil
}
