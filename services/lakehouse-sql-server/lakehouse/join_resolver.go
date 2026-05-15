package lakehouse

import (
	"database/sql"
	"fmt"
)

// ResolveJoinPath discovers JOIN paths between the given objects through
// ont_causality(relation_type='join_key'). Each join_key causality link
// connects two property-anchored Ok entries across different Ods.
//
// Returns an ordered slice of JoinEdges for the SQL builder.
func ResolveJoinPath(db *sql.DB, projectID string, objectNames []string) ([]JoinEdge, error) {
	if len(objectNames) <= 1 {
		return nil, nil
	}

	// 1. Load all join_key edges for this project.
	edges, err := loadJoinEdges(db, projectID)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return nil, fmt.Errorf("没有定义任何 JOIN 关系。请先在属性图谱页面建立 Od 之间的属性关联关系")
	}

	// 2. Build adjacency map: Od name → []JoinEdge (both directions).
	adj := map[string][]JoinEdge{}
	for _, e := range edges {
		adj[e.FromOd] = append(adj[e.FromOd], e)
		// Add reverse edge.
		rev := JoinEdge{
			FromOd: e.ToOd, FromProp: e.ToProp,
			ToOd: e.FromOd, ToProp: e.FromProp,
			Cardinality: reverseCardinality(e.Cardinality),
		}
		adj[e.ToOd] = append(adj[e.ToOd], rev)
	}

	// 3. BFS/greedy to connect all requested objects.
	return findSpanningPath(adj, objectNames)
}

// loadJoinEdges queries ont_causality for all join_key relations.
func loadJoinEdges(db *sql.DB, projectID string) ([]JoinEdge, error) {
	rows, err := db.Query(`
		SELECT c.direction,
		       fp.name AS from_prop, fo.name AS from_od,
		       tp.name AS to_prop,   to_.name AS to_od
		FROM ont_causality c
		JOIN ont_knowledge fk ON c.from_knowledge_id = fk.id AND fk.anchor_type = 'property'
		JOIN ont_property fp ON fk.anchor_id::text = fp.id::text
		JOIN ont_object_type fo ON fp.object_type_id = fo.id
		JOIN ont_knowledge tk ON c.to_knowledge_id = tk.id AND tk.anchor_type = 'property'
		JOIN ont_property tp ON tk.anchor_id::text = tp.id::text
		JOIN ont_object_type to_ ON tp.object_type_id = to_.id
		WHERE c.project_id = $1 AND c.relation_type = 'join_key'
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("加载 JOIN 关系失败: %w", err)
	}
	defer rows.Close()

	var edges []JoinEdge
	for rows.Next() {
		var cardinality, fromProp, fromOd, toProp, toOd string
		if err := rows.Scan(&cardinality, &fromProp, &fromOd, &toProp, &toOd); err != nil {
			continue
		}
		edges = append(edges, JoinEdge{
			FromOd: fromOd, FromProp: fromProp,
			ToOd: toOd, ToProp: toProp,
			Cardinality: cardinality,
		})
	}
	return edges, nil
}

// findSpanningPath uses BFS to connect all target objects through the
// adjacency graph. For 2 objects: direct BFS A→B. For 3+: greedy
// approach starting from the first, connecting nearest unvisited.
func findSpanningPath(adj map[string][]JoinEdge, targets []string) ([]JoinEdge, error) {
	if len(targets) == 2 {
		path, err := bfsPath(adj, targets[0], targets[1])
		if err != nil {
			return nil, fmt.Errorf("无法找到 %s 与 %s 之间的 JOIN 路径，请先在属性图谱页面建立关联关系", targets[0], targets[1])
		}
		return path, nil
	}

	// Greedy: start from first target, always connect to nearest unvisited.
	connected := map[string]bool{targets[0]: true}
	remaining := map[string]bool{}
	for _, t := range targets[1:] {
		remaining[t] = true
	}

	var result []JoinEdge
	for len(remaining) > 0 {
		var bestPath []JoinEdge
		var bestTarget string

		for target := range remaining {
			for node := range connected {
				path, err := bfsPath(adj, node, target)
				if err != nil {
					continue
				}
				if bestPath == nil || len(path) < len(bestPath) {
					bestPath = path
					bestTarget = target
				}
			}
		}

		if bestPath == nil {
			// Collect unreachable targets.
			var unreachable []string
			for t := range remaining {
				unreachable = append(unreachable, t)
			}
			return nil, fmt.Errorf("无法找到到达 %v 的 JOIN 路径，请先在属性图谱页面建立关联关系", unreachable)
		}

		result = append(result, bestPath...)
		delete(remaining, bestTarget)
		// Mark all intermediate nodes as connected too.
		for _, e := range bestPath {
			connected[e.FromOd] = true
			connected[e.ToOd] = true
		}
	}

	return deduplicateEdges(result), nil
}

// bfsPath finds the shortest path from src to dst in the adjacency graph.
func bfsPath(adj map[string][]JoinEdge, src, dst string) ([]JoinEdge, error) {
	if src == dst {
		return nil, nil
	}

	type bfsNode struct {
		od   string
		path []JoinEdge
	}

	visited := map[string]bool{src: true}
	queue := []bfsNode{{od: src}}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		for _, edge := range adj[curr.od] {
			if visited[edge.ToOd] {
				continue
			}
			newPath := make([]JoinEdge, len(curr.path)+1)
			copy(newPath, curr.path)
			newPath[len(curr.path)] = edge

			if edge.ToOd == dst {
				return newPath, nil
			}

			visited[edge.ToOd] = true
			queue = append(queue, bfsNode{od: edge.ToOd, path: newPath})
		}
	}

	return nil, fmt.Errorf("no path from %s to %s", src, dst)
}

// deduplicateEdges removes duplicate edges (same fromOd+fromProp+toOd+toProp).
func deduplicateEdges(edges []JoinEdge) []JoinEdge {
	seen := map[string]bool{}
	var result []JoinEdge
	for _, e := range edges {
		key := e.FromOd + "." + e.FromProp + "->" + e.ToOd + "." + e.ToProp
		revKey := e.ToOd + "." + e.ToProp + "->" + e.FromOd + "." + e.FromProp
		if seen[key] || seen[revKey] {
			continue
		}
		seen[key] = true
		result = append(result, e)
	}
	return result
}

// reverseCardinality flips cardinality direction.
func reverseCardinality(c string) string {
	switch c {
	case "1:N":
		return "N:1"
	case "N:1":
		return "1:N"
	default:
		return c // "1:1" and "N:N" are symmetric
	}
}
