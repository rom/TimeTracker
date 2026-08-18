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
