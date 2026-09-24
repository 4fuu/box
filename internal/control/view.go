package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/4fuu/box/internal/size"
)

func FormatPairing(p Pairing, kind string) string {
	if kind == "node" {
		return fmt.Sprintf("code: %s\nexpires: %s\n", p.Secret, p.Expires.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("one-time password: %s\nexpires: %s\n", p.Secret, p.Expires.UTC().Format(time.RFC3339))
}

func FormatComputers(list []ComputerView, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(list)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tNODE\tIMAGE\tPORTALS")
	for _, c := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.State, c.Node, c.Image, strings.Join(c.Portals, ","))
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func FormatNodes(list []NodeView, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(list)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tONLINE\tCAPACITY\tUSED\tTAGS\tIMAGES")
	for _, n := range list {
		online := "no"
		if n.Online {
			online = "yes"
		}
		cap := fmt.Sprintf("%d cpu, %s, %s", n.CPU, size.FormatBytes(n.Memory), size.FormatBytes(n.Disk))
		used := fmt.Sprintf("%s cpu, %s, %s", size.FormatCPU(n.UsedCPU), size.FormatBytes(n.UsedMemory), size.FormatBytes(n.UsedDisk))
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", n.Name, online, cap, used, formatTags(n.Tags), strings.Join(n.Images, ","))
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func FormatImages(list []ImageView, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(list)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tREF\tDEFAULT\tNODES")
	for _, img := range list {
		def := "no"
		if img.Default {
			def = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", img.Name, img.Ref, def, strings.Join(img.Nodes, ","))
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func FormatKeys(list []KeyView, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(list)
	}
	var b strings.Builder
	for _, k := range list {
		fmt.Fprintf(&b, "%s  %s  %s\n", k.Fingerprint, k.Comment, k.BoundAt.UTC().Format(time.RFC3339))
	}
	return b.String(), nil
}

func FormatDefaults(d Defaults, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(d)
	}
	node := d.Node
	if node == "" {
		node = "-"
	}
	image := d.Image
	if image == "" {
		image = "-"
	}
	return fmt.Sprintf("node\t%s\nimage\t%s\ncpu\t%s\nmemory\t%s\ndisk\t%s\n",
		node, image, size.FormatCPU(d.CPU), size.FormatBytes(d.Memory), size.FormatBytes(d.Disk)), nil
}

func FormatStat(v StatView, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(v)
	}
	net := v.Network
	if v.Network == "ok" {
		net = fmt.Sprintf("rx %d tx %d", v.RX, v.TX)
	}
	return fmt.Sprintf("cpu\t%s\nmemory\t%s\ndisk\t%s\nnetwork\t%s\n",
		size.FormatCPU(v.CPU), size.FormatBytes(v.Memory), size.FormatBytes(v.Disk), net), nil
}

func FormatEnv(names []string, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(struct {
			Names []string `json:"names"`
		}{Names: names})
	}
	return strings.Join(names, "\n") + tern(len(names) > 0, "\n", ""), nil
}

func FormatStatus(st Status, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(st)
	}
	return fmt.Sprintf("domain: %s\nkeys: %d\nnodes: %d\ncomputers: %d\n", st.Domain, st.Keys, st.Nodes, st.Computers), nil
}

func FormatCreated(c Created, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(c)
	}
	return fmt.Sprintf("%s ready\n%s\n%s\n", c.Name, c.HTTP, c.SSH), nil
}

func FormatPortals(list []PortalView, asJSON bool) (string, error) {
	if asJSON {
		return asJSONString(list)
	}
	var b bytes.Buffer
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tPORT")
	for _, p := range list {
		fmt.Fprintf(w, "%s\t%d\n", p.Label, p.Port)
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func formatTags(tags map[string]string) string {
	if len(tags) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func asJSONString(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

func tern(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}
