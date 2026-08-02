# Test Assertions: A Test That Cannot Fail Is Not A Pass

Writing a test from a description of intent produces an assertion that the intended
thing happened. The *discriminating* assertion requires reasoning about the
counterfactual — what would a broken version do, and would this test notice. That
step is reliably skipped. Across one batch of eleven reviewed changes the
implementations were correct and **nine of eleven tests could not fail.**

## Before submitting, run these

1. **Delete your production change and run the new tests.** At least one MUST fail.
   This is not optional and it is one test run.
2. **Run that same revert with your new test removed.** If the suite still goes red,
   your test closed no gap — the coverage already existed. Say so rather than
   claiming a gap was closed.
3. **Confirm the tree still builds before believing any mutation result.** A
   package that fails to compile emits no failures, and the empty output reads
   exactly like a passing suite.
4. **Compare each test's runtime against the delays it configures.** A test that
   finishes faster than the wait it sets up is not exercising the path it claims.
5. **If two tests fail to the same mutation, check their preconditions differ.**
   Two tests for one condition looks like coverage and is not; assert the
   precondition explicitly so the duplication cannot return silently.

## Assertion shapes that pass against the defect

1. Asserting a value is **present** when the property is that another must be
   **absent**. Both directions need asserting.
2. Accepting **either of two orderings** when the property under test is
   determinism.
3. **Sleeping toward a state**, so the assertion passes while nothing has happened.
   A sleep standing in for synchronisation is a silent false pass, not a flake.
4. Building a scenario whose **triggering condition never fires**, so the code
   under test is never reached.
5. Leaving a whole **output channel unasserted** — `io.Discard` on a writer the
   change writes to.
6. An assertion that is **over-determined**: it passes for a reason other than the
   one it names. Break the mechanism the test claims to guard and confirm *that*
   specific break fails it.

## Reporting

Cite only tests that exist in your diff — quoting a test you did not ship is
checked and will fail the change. Report what you ran, never what you expect a run
would have produced.
