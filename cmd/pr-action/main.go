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
	// On PR creation.
	// if BZ found, add external bug tracker for the PR
	// add comment to pr w/ link to BZ
	// if BZ not found comment that this is problemamitic and block ability to merge

	ghToken := githubactions.GetInput("GitHub Token")
	bzToken := githubactions.GetInput("Bugzilla Token")
	orgRepo := githubactions.GetInput("org repo")
	org, repo := splitOrgRepo(orgRepo)

	product = githubactions.GetInput("bz product")
	prNumber, err := strconv.Atoi(githubactions.GetInput("pr number"))
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
		body := fmt.Sprint("Unable to find bug for PR, valid PR titles shoudl look like Bug xxxxxxx: <message>")
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
		body := fmt.Sprintf("Unable to find bug with id: %v. Please make sure the bug if created and valid.", bugID)
		_, _, err := ghClient.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
		}
		os.Exit(1)
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

	for _, pr := range prs {
		fmt.Printf("%#v", pr)
		// if BZ is already associated, exit as everything is OK
		if fmt.Sprintf("%v/%v/pull/%v", pr.Org, pr.Repo, pr.Num) == prID {
			fmt.Printf("PR is found already on valid bug")
			os.Exit(0)
		}
	}
	complete, err := bzClient.AddPullRequestAsExternalBug(bugID, org, repo, prNumber)
	if err != nil || !complete {
		//TODO: GH comment saying this failed
		fmt.Printf("%#v", err)
		fmt.Printf("Unable to add PR to BZ")
		body := fmt.Sprintf("Bug was not moved to POST but was valid. Something went wrong")
		_, resp, err := ghClient.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		fmt.Printf("\n%#v", resp)
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
		}
		os.Exit(1)
	}
	if complete {
		body := fmt.Sprintf("Valid [bug %v](https://bugzilla.redhat.com/show_bug.cgi?id=%v)", bugID, bugID)
		_, _, err = ghClient.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
		}
	}
	// Move BZ to POST if not in post
	if bug.Status != "POST" {
		err := bzClient.UpdateBug(bugID, bugzilla.BugUpdate{
			Status: "POST",
		})
		if err != nil {
			fmt.Printf("\nUnable to move bug to POST")
			fmt.Printf("%v", err)
			body := fmt.Sprintf("Bug was not moved to POST but was valid. Something went wrong")
			_, _, err := ghClient.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
				Body: &body,
			})
			if err != nil {
				fmt.Printf("gh Client error: %v", err)
			}
		}
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
	if !strings.EqualFold(bug.Product, product) {
		fmt.Printf("Invalid product: %v for %v", bug.Product, product)
		body := "Bug is not for a valid product, double check the bug is correct"
		_, _, err := gh.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
			Body: &body,
		})
		if err != nil {
			fmt.Printf("gh Client error: %v", err)
		}
		return false
	}

	// If the target release is not set.
	for _, tr := range bug.TargetRelease {
		if tr == "---" {
			body := fmt.Sprintf("Bug [%v](http://bugzilla.redhat.com/show_bug.cgi?id=%v) does not have a target release set", bug.ID, bug.ID)
			_, _, err := gh.Issues.CreateComment(context.TODO(), org, repo, prNumber, &github.IssueComment{
				Body: &body,
			})
			if err != nil {
				fmt.Printf("gh Client error: %v", err)
			}
			return false
		}
	}

	switch strings.ToUpper(bug.Status) {
	case "NEW", "ASSIGNED", "POST":
		return true
	default:
		fmt.Printf("Invalid bug state: %v", bug.Status)
		body := fmt.Sprintf("Bug is not valid because it is in %v and should be one of NEW, ASSIGNED, or POST", bug.Status)
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
