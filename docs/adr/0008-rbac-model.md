# ADR-0008: Four roles scoped by project membership

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-005, ASR-008

## Context

Server mode has several distinct kinds of user: the person who runs the instance,
team leads who own client relationships and approve timesheets, individual
contributors, and — valuably — clients who should be able to see what they are being
billed for without being able to see anything else.

A role alone is not enough. "Manager" does not mean "can see every customer"; it
means "can see the customers they are responsible for". Authorisation therefore has
two dimensions: what kind of user you are, and which projects you are attached to.
Getting this wrong in either direction is bad: too coarse and a manager reads a
competitor engagement they have no business seeing; too fine and the system becomes
unconfigurable and people grant admin to make it work.

## Decision

Four roles, combined with **per-project membership**:

| Role | Scope |
|---|---|
| **admin** | Everything: users, roles, customers, settings, all data, all money. |
| **manager** | Customers and projects they are a member of: full read/write on the team's entries, approves timesheets, may propose proxy entries for team members, sees rates and amounts. |
| **member** | Own time and expenses; reads the projects they are a member of; may propose proxy entries for colleagues on shared projects; sees own rates only. |
| **client** | Read-only reports and exports for their own customer, confirmed entries only, no personnel detail beyond who did the work, no internal notes. |

The authorisation decision is a single function, `Can(ctx, action, resource)`,
implemented in one package and called by every service method. Handlers do not make
authorisation decisions; they render what the service returns. The UI hides what a
user may not do, but hiding is presentation, never enforcement — every action is
checked again server-side, and the tests assert exactly that.

Defaults are least-privilege: a newly created or newly SSO-provisioned user is a
`member` with no project memberships, and therefore can see nothing but their own
(empty) timesheet until someone grants access.

The `client` role gets a deliberately narrowed projection of the data, not a
filtered view of the full record — internal notes, cost rates, and colleague
identities are removed before the data leaves the service layer, so a template bug
cannot leak them.

**Implemented in 2026-08, and the gap between writing that paragraph and making
it true is worth recording.** The scope existed and the role existed; the
projection did not, and neither did any way for a client to read anything. Three
things were found by asking what a client actually receives, rather than by
reading the checks:

* `GetUser` — the query that resolves the acting user on *every request* — did
  not select `client_customer_id`. Every client arrived with a customer of zero,
  which the authoriser reads as "scoped to no customer" and refuses, so the role
  could sign in and then be refused every screen. One column, and the entire
  role.
* A client could **download a backup**. The check on it asked whether the actor
  may view their customer's time, which a client may; the archive came back
  carrying the catalogue, including the customer's negotiated hourly rate.
* A client could open the **catalogue screen**, which showed that same rate in a
  table, beside forms to create and archive records they could not submit.

The lesson is the one the paragraph above already implies and did not enforce: a
permission check answers the question it was asked. "May this actor view this
customer's time" is not "may this actor have everything a backup contains". The
projection is now applied to entries, customers and projects; a source-level test
(`internal/service/architecture_test.go`) fails the build if a new service method
returns entries without it.

**And the same lesson again in 2026-10, one layer down.** A coverage sweep asked
what a *member with no project memberships* receives, and found that the scope
was enforced by what each call site remembered to put in the resource:

* `requireMembership` permits a resource that names no project, which is right
  for one that genuinely has none — the instance settings, a user account, or a
  listing that `effectiveScope` narrows instead. `entryResource` built a time
  entry with no project id, and the customer and audit-trail resources carried
  none either, so "may this member read this entry" was answered as though it
  had been asked about a global setting. Another user's entry came back with its
  note and its billing snapshot, any customer with its rate, and the audit trail
  of any record of any type with the values each change moved between. It needed
  an id, and ids are sequential.
* The `client` branch required that the resource name *a* customer, not *their*
  customer. It was correct only because every call site passed the acting
  client's own id.
* The backup screen asked whether the caller may view time entries, which such a
  member may — and then showed them the archives on the server's disk.

Three changes, and the third is the one that matters beyond these cases.
Resources now say where they sit; a customer, which has no single project, is
scoped by membership of any of its projects; and **a named record of a
project-scoped type that says nothing about where it sits is refused rather than
permitted**. The permissive branch stays — a global setting still needs it — but
it can no longer be reached by forgetting a field, which is the only way it was
ever reached. A failing membership lookup propagates as an error rather than as
a refusal, because a refusal reaches the caller as "not found" and a database
that is merely unreachable should not read as data loss.

**And a third time, at the screens.** The navigation has never offered a client
more than the entries listing and the guide, and its comment gives the reason —
"offering a link whose only outcome is a refusal is a worse answer than not
offering it" — along with the caveat that the data behind those screens is
narrowed regardless, "this is presentation, and presentation is never the
enforcement". For nine screens it was the only thing there, and typing the URL
reached them:

* `/routines`, `/move`, `/import`, `/calendar` and `/expenses` rendered live
  forms for recording time a client does not record. Dead rather than
  disclosing, but a page of controls that each answer 403 is not read-only, it
  is broken.
* `/tags` listed the whole instance's vocabulary. Tag names carry other
  customers' work in them, and the projection strips tags from a client's
  entries for exactly that reason, so the screen said the opposite of the
  projection. The correction form's datalist did too.
* The **edit forms** — for an entry, a customer, a project and an assignment —
  loaded their record with a *view* check while the save they submit to requires
  update or manage. So a client was handed an assignment's hourly rate and a
  project's budget typed into form fields, from the unnarrowed record, because a
  form cannot be narrowed: it writes its fields back. A member on the project
  got the same.
* `/settings` asked nothing at all, and carries the instance's default hourly
  rate. Any member read it there.
* `/attachments/{id}` served a receipt in full to anybody who could view the
  record it hung on. The projection drops the attachment count and states the
  premise plainly — "a client cannot fetch an attachment" — and that was not
  true.

Each is now decided rather than discovered: a screen about recording somebody's
own time redirects a client to their listing, an administrative one refuses, an
edit form asks the question its save asks, and a client fetches no attachment at
all.

**And then the same question of a member**, which the role table answers and the
code did not:

* The **catalogue screen** showed every customer's negotiated rate and every
  project's budget, because the only narrowing that existed was the client's and
  nothing applied it to anybody else - while the table says a member "sees own
  rates only". The screen stays theirs, since the table also says they read the
  projects they are a member of and it is where a favourite is marked, but the
  figures are removed from the value before it leaves the service and the
  create, edit and archive controls are left out of the page. Who may see money
  is asked of the authoriser (`ActionViewMoney`) rather than written as a role
  test, so the answer comes from the table rather than from a second copy of it.
  Assignments had never been narrowed for any role, and an assignment's rate is
  the most specific of the three.
* The **tag vocabulary** was the whole instance's, names and counts. A tag is
  created by being used, so the vocabulary describes the work it labels:
  "globex-escalation" with forty entries behind it is a sentence about somebody
  else's engagement. It is scoped like the entries listing now, so the labels a
  filter offers are the labels it can match and a count agrees with the rows
  behind it. The administrative listing stays whole, because tidying a
  vocabulary means seeing all of it.
* The **navigation** had applied its own rule - don't offer a link whose only
  outcome is a refusal - to a client alone, so a member was offered the tag
  editor and the user list. Both refuse them.

One more thing fell out of asking: `scopeFor` fills `OwnUserID` and documents
"member - the projects they are a member of, **plus their own records**", and
`Scope.condition` never rendered it. An account with no memberships - which this
ADR says is what every new and every SSO-provisioned user starts as, seeing
"their own (empty) timesheet" - could record time and then find every screen
empty, because an empty project list renders as `1 = 0` and nothing re-admitted
their own rows. The own-records dimension is now a column the caller names
explicitly, including as "": a member's timesheet includes the hour they
recorded off-project, and a project's *budget* does not appear to them because
of it. The lesson is the same one twice over — a check answers the question it was
asked, and a form that edits a record is not a read of it — and the guard is a
sweep (`internal/web/clientscreens_test.go`) that walks the route table and
requires a decided answer for every GET, so a screen added later cannot be
reachable merely because no handler thought to ask.

## Consequences

**Positive**

* Covers the real organisational shapes without a permission editor.
* One enforcement point makes the security-relevant code small enough to review and
  to test exhaustively.
* Client access becomes a feature rather than an emailed PDF.

**Negative / accepted costs**

* Four roles will not fit everyone. A "finance can see all money but no notes" user
  has no home here, and would need a new ADR introducing named permissions.
* Every query that lists data must be membership-scoped, which is easy to forget;
  mitigated by making the repository take the actor's scope as a parameter rather
  than offering an unscoped list at all.
* Every read of a single record must say where that record sits in the
  hierarchy, which is just as easy to forget and was forgotten three times. Now
  mitigated the same way the listings are: the authoriser refuses a named record
  of a project-scoped type that does not say, so forgetting fails closed instead
  of opening up.
* A screen is not an action, so nothing about the four verbs says who may open
  one. Nine screens were reachable by a client because the navigation hid them
  and no handler asked. Mitigated by a sweep over the route table rather than by
  a mechanism: every GET must have a decided answer for a client, and a new
  route with no answer fails the build.
* The client projection is a second shape of the same data to maintain and test.

## Alternatives considered

**Admin/member only** — much less to build. Rejected: no approval hierarchy, and no
way to let a client see their own report without giving them the whole instance.

**Fine-grained named permissions bundled into editable roles** — the most flexible
and most auditable design, and where this will go if the four roles prove too
coarse. Rejected for now as premature: it moves the security policy into runtime
configuration, where it can be misconfigured, and it cannot be tested exhaustively
the way a fixed matrix can.

**Attribute-based access control (policy engine)** — powerful, and complete overkill
for a timesheet app; the policy language would be the hardest thing in the codebase,
against ASR-011.

## Related

* ADR-0005 (proxy entries), ADR-0006 (authentication), ADR-0012 (service boundary)
