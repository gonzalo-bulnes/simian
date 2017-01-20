package simian

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const nodeEntriesDir = "entries"
const nodeFingerprintFile = "fingerprint"

var errLastEntry = errors.New("last entry")

type IndexNode struct {
	path        string
	fingerprint Fingerprint
}

func (node *IndexNode) Add(entry *IndexEntry, childFingerprintSize int, index *Index) (*IndexNode, error) {

	fmt.Printf("Node[%s] Add %d\n", node.path, childFingerprintSize)

	entryFingerprint := entry.FingerprintForSize(childFingerprintSize)

	if !node.HasChildren() {

		// We can go deeper and this new entry is sufficiently different to
		// the rest, so split this leaf node by turning entries into children.
		fmt.Printf("Max Diff: %f\n", node.maxChildDifferenceTo(entry.MaxFingerprint))
		if childFingerprintSize < index.maxFingerprintSize && node.maxChildDifferenceTo(entry.MaxFingerprint) > index.maxEntryDifference {
			node.pushEntriesToChildren(childFingerprintSize)

		} else {
			err := node.addEntry(entryFingerprint, entry)
			if err != nil {
				return nil, err
			}
			return node, nil
		}
	}

	child, err := node.childWithFingerprint(entryFingerprint, true)
	if err != nil {
		return nil, err
	}

	return child.Add(entry, childFingerprintSize+1, index)
}

func (node *IndexNode) FindNearest(entry *IndexEntry, childFingerprintSize int, maxResults int, maxDifference float64) ([]*IndexEntry, error) {
	results := make([]*IndexEntry, 0, maxResults)

	_, err := node.gatherNearest(entry, childFingerprintSize, maxDifference, &results)
	if err != nil {
		return nil, err
	}

	return results, nil
}

func (node *IndexNode) HasChildren() bool {
	dir, err := os.Open(node.path)
	if err != nil {
		return false
	}
	defer dir.Close()

	for fileInfos, err := dir.Readdir(1); err == nil && len(fileInfos) > 0; fileInfos, err = dir.Readdir(1) {
		for _, info := range fileInfos {
			if info.IsDir() && info.Name() != nodeEntriesDir {
				return true
			}
		}
	}

	return false
}

func (node *IndexNode) addSimilarEntriesTo(entries *[]*IndexEntry, fingerprint Fingerprint, maxDifference float64) (finished bool) {
	finished = false

	fmt.Printf("addSimilarEntriesTo\n")

	node.withEachEntry(func(entry *IndexEntry) error {
		if len(*entries) >= cap(*entries) {
			fmt.Printf("Max results hit\n")
			finished = true
			return errLastEntry
		}

		diff := entry.MaxFingerprint.Difference(fingerprint)
		if diff <= maxDifference {
			fmt.Printf("Found %d of difference %f\n", len(*entries), diff)
			*entries = append(*entries, entry)
		} else {
			fmt.Printf("Max difference hit at %f\n", diff)
			finished = true
			return errLastEntry
		}

		return nil
	})

	return
}

func (node *IndexNode) addEntry(f Fingerprint, entry *IndexEntry) error {
	entriesDir := filepath.Join(node.path, nodeEntriesDir)
	os.Mkdir(entriesDir, os.ModePerm)

	return entry.saveToDir(entriesDir)
}

func (node *IndexNode) allChildren() ([]*IndexNode, error) {
	var children []*IndexNode

	dir, err := os.Open(node.path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	for fileInfos, err := dir.Readdir(1); err == nil && len(fileInfos) > 0; fileInfos, err = dir.Readdir(1) {
		for _, info := range fileInfos {
			if info.IsDir() && info.Name() != nodeEntriesDir {
				child, err := node.loadChild(info.Name())
				if err != nil {
					return nil, err
				}

				children = append(children, child)
			}
		}
	}

	return children, nil
}

func (node *IndexNode) childPathForFingerprint(f Fingerprint) string {
	fingerprintHash := sha256.Sum256(f.Bytes())
	childDirName := hex.EncodeToString(fingerprintHash[:8])
	return filepath.Join(node.path, childDirName)
}

func (node *IndexNode) childWithFingerprint(f Fingerprint, create bool) (*IndexNode, error) {

	childPath := node.childPathForFingerprint(f)
	_, err := os.Stat(childPath)

	// Child doesn't already exist so create it if requested
	if os.IsNotExist(err) {
		if !create {
			return nil, nil
		}

		os.Mkdir(childPath, os.FileMode(0522))

		// Save the actual (non-truncated) fingerprint
		childFingerprintFile := filepath.Join(childPath, nodeFingerprintFile)
		file, err := os.Create(childFingerprintFile)
		if err != nil {
			return nil, err
		}

		defer file.Close()

		_, err = file.Write(f.Bytes())
		if err != nil {
			return nil, err
		}

	} else if err != nil {
		// Some other error - child couldn't be stat'd
		return nil, err
	}

	return &IndexNode{path: childPath, fingerprint: f}, nil
}

func (node *IndexNode) deleteEntries() error {
	entriesDir := filepath.Join(node.path, nodeEntriesDir)
	return os.RemoveAll(entriesDir)
}

func (node *IndexNode) fingerprintForChild(childPath string) (Fingerprint, error) {
	childFingerprint := Fingerprint{}
	childFingerprintFile := filepath.Join(childPath, nodeFingerprintFile)

	file, err := os.Open(childFingerprintFile)
	if err != nil {
		return childFingerprint, err
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return childFingerprint, err
	}

	fingerprintBytes := make([]byte, fileInfo.Size(), fileInfo.Size())
	_, err = file.Read(fingerprintBytes)
	if err != nil {
		return childFingerprint, err
	}

	childFingerprint.UnmarshalBytes(fingerprintBytes)

	return childFingerprint, nil
}

func (node *IndexNode) gatherNearest(entry *IndexEntry, childFingerprintSize int, maxDifference float64, results *[]*IndexEntry) (bool, error) {

	fmt.Printf("%d gatherNearest\n", childFingerprintSize)

	// Check for an exact matching child
	entryFingerprint := entry.FingerprintForSize(childFingerprintSize)
	exactChild, _ := node.childWithFingerprint(entryFingerprint, false)

	// One exists - recursively search it
	if exactChild != nil {
		finished, err := exactChild.gatherNearest(entry, childFingerprintSize+1, maxDifference, results)
		if err != nil {
			return false, err
		}

		if finished || exactChild.addSimilarEntriesTo(results, entry.MaxFingerprint, maxDifference) {
			fmt.Printf("%d finished gatherNearest\n", childFingerprintSize)
			return true, nil
		}
	}

	// Need more results - find and sort all children by nearness
	children, err := node.allChildren()
	if err != nil {
		return false, err
	}
	sort.Sort(nodesByDifferenceToFingerprintWith(children, entryFingerprint))

	fmt.Printf("Sorting %d children...\n", len(children))
	for i, child := range children {
		diff := child.fingerprint.Difference(entryFingerprint)
		fmt.Printf("%d sorted child %d of %f (%d %d)\n", childFingerprintSize+1, i, diff, len(child.fingerprint.samples), len(entryFingerprint.samples))
	}

	// Recursively gather from nearest children
	for i, child := range children {
		fmt.Printf("Visiting child %d\n", i)
		if exactChild != nil && child.path == exactChild.path {
			continue
		}

		finished, err := child.gatherNearest(entry, childFingerprintSize+1, maxDifference, results)
		if err != nil {
			return false, err
		}

		if finished || child.addSimilarEntriesTo(results, entry.MaxFingerprint, maxDifference) {
			fmt.Printf("%d finished gatherNearest\n", childFingerprintSize)
			return true, nil
		}
	}

	fmt.Printf("%d finished gatherNearest - no more to gather\n", childFingerprintSize)
	return false, nil
}

func (node *IndexNode) loadChild(childDirName string) (*IndexNode, error) {
	childPath := filepath.Join(node.path, childDirName)
	childFingerprint, err := node.fingerprintForChild(childPath)
	if err != nil {
		return nil, err
	}

	return &IndexNode{path: childPath, fingerprint: childFingerprint}, nil
}

func (node *IndexNode) maxChildDifferenceTo(f Fingerprint) float64 {
	maxDifference := 0.0

	node.withEachEntry(func(entry *IndexEntry) error {
		diff := entry.MaxFingerprint.Difference(f)
		maxDifference = math.Max(diff, maxDifference)
		return nil
	})

	return maxDifference
}

func (node *IndexNode) pushEntriesToChildren(childFingerprintSize int) error {
	node.withEachEntry(func(entry *IndexEntry) error {
		entryFingerprint := entry.FingerprintForSize(childFingerprintSize)
		child, err := node.childWithFingerprint(entryFingerprint, true)
		if err != nil {
			return err
		}
		child.addEntry(entryFingerprint, entry)
		return nil
	})

	return node.deleteEntries()
}

func (node *IndexNode) withEachEntry(action func(*IndexEntry) error) error {
	entriesDir := filepath.Join(node.path, nodeEntriesDir)

	dir, err := os.Open(entriesDir)
	if err != nil {
		return err
	}
	defer dir.Close()

	for fileInfos, err := dir.Readdir(1); err == nil && len(fileInfos) > 0; fileInfos, err = dir.Readdir(1) {
		for _, fileInfo := range fileInfos {
			if strings.HasSuffix(fileInfo.Name(), ".entry") {
				entry, err2 := NewIndexEntryFromFile(filepath.Join(entriesDir, fileInfo.Name()))
				if err2 == nil {
					err3 := action(entry)
					if err3 == errLastEntry {
						return nil
					}
					if err3 != nil {
						return err3
					}
				}
			}
		}
	}

	return nil
}

type nodesByDifferenceToFingerprint struct {
	nodes       []*IndexNode
	differences []float64
}

func (sorter *nodesByDifferenceToFingerprint) Len() int {
	return len(sorter.nodes)
}

func (sorter *nodesByDifferenceToFingerprint) Less(i, j int) bool {
	return sorter.differences[i] < sorter.differences[j]
}

func (sorter *nodesByDifferenceToFingerprint) Swap(i, j int) {
	tmpNode := sorter.nodes[i]
	sorter.nodes[i] = sorter.nodes[j]
	sorter.nodes[j] = tmpNode

	tmpDiff := sorter.differences[i]
	sorter.differences[i] = sorter.differences[j]
	sorter.differences[j] = tmpDiff
}

func nodesByDifferenceToFingerprintWith(nodes []*IndexNode, f Fingerprint) *nodesByDifferenceToFingerprint {
	differences := make([]float64, len(nodes), len(nodes))
	for i, n := range nodes {
		differences[i] = n.fingerprint.Difference(f)
	}

	return &nodesByDifferenceToFingerprint{nodes: nodes, differences: differences}
}