package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
)

func recoverJob(args []string, stdout, stderr io.Writer) int {
	return recoverManaged("job", args, stdout, stderr)
}

func recoverManaged(kind string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !managed.ValidRequestKey(args[0]) {
		fmt.Fprintf(stderr, "usage: haepari %s recover <id> --plan --json | --plan-token <token> --request-key <key> --json\n", kind)
		return 2
	}
	id := args[0]
	flags := flag.NewFlagSet(kind+" recover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	plan := flags.Bool("plan", false, "inspect a read-only recovery plan")
	action := flags.String("action", "", "reconciliation or scoped attestation plan")
	token := flags.String("plan-token", "", "apply one current plan")
	key := flags.String("request-key", "", "saved permanent mutation key")
	evidencePath := flags.String("evidence-file", "", "scoped operator evidence document")
	outputJSON := flags.Bool("json", false, "machine-readable response")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !*outputJSON {
		return 2
	}
	q := managed.RecoveryRequest{Plan: *plan, Action: *action, PlanToken: *token, MutationKey: *key}
	limit := 100
	if kind == "resource" {
		limit = 90
	}
	validAction := *action == "" || kind == "resource" && (*action == "recollect" || *action == "dispose") || kind == "job" && (*action == "reconcile" || *action == "attest-stopped" || *action == "attest-no-start")
	if len(*key) > limit || kind == "resource" && *evidencePath != "" || !validAction {
		fmt.Fprintln(stderr, "invalid recovery action, evidence flags or request key length")
		return 2
	}
	if *plan && (*token != "" || *key != "" || *evidencePath != "") || !*plan && (*token == "" || !managed.ValidRequestKey(*key) || *action != "") {
		fmt.Fprintln(stderr, "choose read-only plan or apply with the saved token and request key")
		return 2
	}
	if *evidencePath != "" {
		raw, err := readSpec(*evidencePath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		q.Evidence = new(managed.RecoveryEvidence)
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if managed.ValidateUniqueJSONKeys(raw) != nil || d.Decode(q.Evidence) != nil {
			fmt.Fprintln(stderr, "invalid recovery evidence")
			return 2
		}
		var tail any
		if d.Decode(&tail) != io.EOF {
			fmt.Fprintln(stderr, "invalid recovery evidence")
			return 2
		}
		if managed.ValidateRecoveryEvidence(*q.Evidence) != nil || q.Evidence.Source != "operator-attested" {
			fmt.Fprintln(stderr, "invalid scoped operator evidence")
			return 2
		}
	}
	c, code := connect(stderr)
	if c == nil {
		return code
	}
	defer c.Close()
	var envelope client.Envelope
	var err error
	if kind == "resource" {
		envelope, err = c.RecoverResource(context.Background(), id, q)
	} else {
		envelope, err = c.RecoverJob(context.Background(), id, q)
	}
	if err != nil {
		return clientFailure(stdout, stderr, envelope, err)
	}
	return emit(stdout, stderr, envelope)
}
