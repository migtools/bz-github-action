package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/go-github/v33/github"
	"github.com/konveyor/bz-github-action/pkg/bugzilla"
	"github.com/sethvargo/go-githubactions"
)

var product string

func main() {

	ghToken := githubactions.GetInput("GitHub Token")
	bzToken := githubactions.GetInput("Bugzilla Token")
	orgRepo := githubactions.GetInput("org repo")
	org, repo := splitOrgRepo(orgRepo)

	branchToTargetRelease, err := getBranchToTargetRelease(githubactions.GetInput("branch to release"))
	if err != nil {
		fmt.Printf("\nUnable to complete action: %v", err)
	}

	product = githubactions.GetInput("bz product")
	prNumber, err := strconv.Atoi(githubactions.GetInput("pr number"))
	prBaseBranch := githubactions.GetInput("base branch")
	if err != nil {
		fmt.Printf("Invalid PR number")
	}
	client := &http.Client{Transport: &ghAuthRoundTripper{
		RoundTripper: http.DefaultTransport,
		ghToken:      ghToken,
	}}

	ghClient := github.NewClient(client)

	bzTokenFunc := func() []byte {
		return []byte(bzToken)
	}

	// Setup BZ client
	// TODO: paramertize values
	bzClient := bugzilla.NewClient(bzTokenFunc, "https://bugzilla.redhat.com", 131)

	bugID := getBugID(githubactions.GetInput("title"))

	if bugID == 0 {
		fmt.Printf("\nNo bug associated with this PR")
		os.Exit(0)
	}

	if bugID == 1 {
		fmt.Printf("\nUnable to get BZ id for PR")
		body := fmt.Sprint("Unable to find bug for PR. Valid PR titles should look like Bug xxxxxxx: <message>")
		ghClient.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		os.Exit(1)
	}

	bug, err := bzClient.GetBug(bugID)
	if err != nil {
		// GH add comment to this effect
		fmt.Printf("%#v", err)
		fmt.Printf("\nUnable to retrieve bug")
		body := fmt.Sprintf("Unable to find bug with id: %v. Please make sure the bug is created and valid.", bugID)
		_, _, err := ghClient.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
		}
		os.Exit(1)
	}

	if !inTargetRelease(branchToTargetRelease[prBaseBranch], bug.TargetRelease) {
		fmt.Printf("Merging fix for the branch: %v which is not associated with the target release: %v", prBaseBranch, bug.TargetRelease)
		body := fmt.Sprintf("Unable to find the base branch: %s in target releases for bug: %s", branchToTargetRelease[prBaseBranch], strings.Join(bug.TargetRelease, ", "))
		_, _, err := ghClient.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if !validBug(bug, ghClient, org, repo, prNumber) {
		// GH add comment to this effect
		fmt.Printf("\nNo valid bug associated with this PR")
		os.Exit(1)
	}

	// Determine Associated PR's with github issue number
	prs, err := bzClient.GetExternalBugPRsOnBug(bugID)
	if err != nil {
		//TODO: GH comment saying this failed
		fmt.Printf("Unable to add PR to BZ")
		os.Exit(1)
	}
	// get PR ref id
	prID := fmt.Sprintf("%v/%v/pull/%v", org, repo, prNumber)
	lastPR := true
	foundAttached := false

	for _, pr := range prs {
		if fmt.Sprintf("%v/%v/pull/%v", pr.Org, pr.Repo, pr.Num) != prID {
			// Find PR from github
			// Determine if closed or not
			// If closed (either merged or not) it should still no longer factor.
			pull, _, err := ghClient.PullRequests.Get(context.TODO(), pr.Org, pr.Repo, pr.Num)
			if err != nil {
				fmt.Printf("\nUnable to query github for the pr: %v/%v/pull/%v", pr.Org, pr.Repo, pr.Num)
				os.Exit(1)
			}
			if pull.Merged != nil && *pull.Merged != true {
				lastPR = false
			}
		}
		if fmt.Sprintf("%v/%v/pull/%v", pr.Org, pr.Repo, pr.Num) == prID {
			foundAttached = true
		}
	}
	if lastPR && foundAttached {
		err := bzClient.UpdateBug(bugID, bugzilla.BugUpdate{
			Status: "MODIFIED",
		})
		if err != nil {
			fmt.Printf("\nUnable to move bug to modified")
			os.Exit(1)
		}
		fmt.Printf("\nSuccessfully moved BZ to modified")

	} else if !foundAttached {
		// Error out and add to PR that this was not attached to BZ
		fmt.Printf("\nUnable to find bz that has this PR attached. not doing anything in this case")
		os.Exit(1)
	} else {
		fmt.Printf("\nNot moving to modified because not the last PR attached to the BZ")
	}
}

type ghAuthRoundTripper struct {
	http.RoundTripper
	ghToken string
}

func (g *ghAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+g.ghToken)
	return g.RoundTripper.RoundTrip(req)
}

func getBugID(prTitle string) int {
	re := regexp.MustCompile(`[b,B]ug \d+:`)

	bug := re.FindString(prTitle)
	if bug != "" {
		bugParts := strings.Split(bug, " ")
		if len(bugParts) < 2 {
			fmt.Printf("\nUnable to find bug id in bug substring: %v, must look like Bug xxxxxxxx:", bug)
			return 1
		}
		id, err := strconv.Atoi(strings.Replace(bugParts[1], ":", "", -1))
		if err != nil {
			fmt.Printf("\nUnable to convert bug id: %v to an valid value", bugParts[1])
			return 1
		}
		return id
	}

	fmt.Printf("\nUnable to find bug id: %v", bug)
	return 0
}

// validBug will determine and alert the user that the bz is not valid
func validBug(bug *bugzilla.Bug, gh *github.Client, org, repo string, prNumber int) bool {
	if strings.ToUpper(bug.Product) != strings.ToUpper(product) {
		fmt.Printf("Invalid product: %v for %v", bug.Product, product)
		body := fmt.Sprintf("Bug is not for a valid product, double check the bug is correct")
		_, _, err := gh.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
		}
		return false
	}
	switch strings.ToUpper(bug.Status) {
	case "NEW", "ASSIGNED", "POST":
		return true
	default:
		fmt.Printf("Invalid bug state: %v", bug.Status)
		body := fmt.Sprintf("Bug is not invalid because it is in: %v and should be one of new, assigned or post", bug.Status)
		_, _, err := gh.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
		}
		return false
	}
}

func splitOrgRepo(orgRepo string) (org, repo string) {
	parts := strings.Split(orgRepo, "/")
	org = strings.Replace(parts[0], "/", "", -1)
	repo = strings.Replace(parts[1], "/", "", -1)
	return
}

func getBranchToTargetRelease(branchToTargetRelease string) (map[string]string, error) {
	parts := strings.Split(branchToTargetRelease, ",")

	branchToTargetReleaseMap := map[string]string{}
	for _, part := range parts {
		parts := strings.Split(part, ":")
		if len(parts) < 2 {
			return branchToTargetReleaseMap, fmt.Errorf("Unable to make branch and release mapping for: %v", part)
		}
		branchToTargetReleaseMap[strings.Replace(parts[0], ":", "", -1)] = strings.Replace(parts[1], ":", "", -1)
	}
	return branchToTargetReleaseMap, nil
}

func inTargetRelease(targetRelease string, targetReleases []string) bool {
	for _, tr := range targetReleases {
		if targetRelease == tr {
			return true
		}
	}
	return false
}
