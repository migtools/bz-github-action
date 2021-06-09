# Adding the Bugzilla Automation to your Repo

To add the BZ automation, copy the files in actions to `.github/workflows/`.

Change the values that you need to change:

* Change the `bz_product` to your bugzilla product name
* Change the `branch_to_release` to your branch and target releases in BZ.

## Notable Logic:

### On Pull Request Creation
* On PR creation, when you add `Bug XXXXXX:` to your PR title the bug will be linked to the BZ with id xxxxxx.
* The bug will also be moved to `POST` status.
* The automation will only attach the PR if the bug is in `NEW`, `ASSIGNED`, or `POST` status. If it is another status you will need to move the bug back to one of those statues and re-run the automation from the actions tab.

### On Pull Request Merge
* On PR merge, we will determine if there are other PR's associated with the bug.
* If there are more open PRs then we will leave the bug in `POST`.
* If it is the last open PR, it will then check the target release of the BZ is the same as the release branch that you are merging to.
* If it is not the correct release branch the bug will stay in `POST`.
* If it is the correct release branch the bug will be moved to `MODIFIED`

### Notes: 

The workflows must be on every release branch to have the automation run correctly.
