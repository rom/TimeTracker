# ADR-0016: Listing queries take the actor's scope

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-005

## Context

[ADR-0008](0008-rbac-model.md) settled *who* may do *what*. It left open a
narrower question that turns out to matter more in practice: how a listing query
knows which rows the actor may see.

The obvious approach is to query everything and filter afterwards - in the
service layer, or in the template. Both work, and both fail the same way. A
listing is added, the filter is forgotten, and a manager sees a competitor
engagement. Nothing about the code looks wrong; the leak is an absence.

There is a second, sharper hazard. If "no scope" is represented by an empty
value, then a bug that fails to populate the scope produces *unrestricted*
results. The failure direction is exactly backwards.

## Decision

Every listing query in `internal/store` takes a `Scope` parameter. There is no
unscoped variant to call by accident.

`Scope` renders itself into a SQL condition, and the empty scope renders as
`1 = 0` - **match nothing**. A user who belongs to no projects sees nothing; a
bug that forgets to populate the scope shows an empty screen rather than
everybody's data. Only `UnrestrictedScope()`, which the service layer constructs
solely for an administrator, lifts the restriction.

The service layer derives the scope from the actor's role and memberships in one
place (`scopeFor`), and the authorisation check still runs first: the scope
narrows *which rows*, `Can` decides *whether to ask at all*.

## Consequences

**Positive**

* Filtering happens in SQL, so a template bug cannot leak a row that was never
  fetched.
* The failure mode is an empty list, which someone reports immediately, rather
  than a silent over-disclosure nobody notices.
* Adding a listing query forces a decision about scope, because the parameter
  cannot be omitted.

**Negative / accepted costs**

* Every listing signature is one parameter longer, including in local mode where
  the answer is always "unrestricted".
* The scope must be threaded from the service layer into each query, and a
  method that forgets to pass the actor's scope returns nothing - which is safe
  but confusing until the cause is found. This happened during development: the
  day view went blank the moment scoping landed, precisely because it had not
  been updated yet.
* A user's memberships are read on each listing request. It is one indexed query
  and has not been worth caching.

## Alternatives considered

**Filter in the service layer after an unscoped query** - simpler signatures.
Rejected: it fetches rows the actor may not see, so the protection is a line of
Go that can be forgotten rather than a property of the query.

**Filter in the template** - rejected outright; it puts a security decision in
the least tested layer.

**A row-level security policy in the database** - impossible to bypass from
application code, and genuinely attractive. Rejected because SQLite has no such
feature, and emulating it with views and triggers would be harder to read than
the thing it replaces (ASR-011).

## Related

* ADR-0008 (RBAC), ADR-0012 (layered packages)
