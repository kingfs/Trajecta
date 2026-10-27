import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  allDirectoryIDs,
  buildResourceForest,
  classifyURLHost,
  countForestLeaves,
  defaultExpandedIDs,
  splitResourcePath,
} from "../src/lib/resourceTree.js";

/*
 * The resources page shows the index as directory trees rather than as a list,
 * so the aggregation has to be right: a directory row carries the total of
 * everything below it, every root a path can have stays its own tree, and a URL
 * is grouped by what its authority actually is. These tests pin those decisions.
 */

const path = (value, count = 1) => ({ kind: "local_path", value, access_count: count });
const url = (value, count = 1) => ({ kind: "url", value, access_count: count });

function names(nodes) {
  return nodes.map((node) => node.name);
}

function at(node, ...segments) {
  let current = node;
  for (const segment of segments) {
    const next = current.children.find((child) => child.name === segment);
    assert.ok(next, `missing child ${segment} under ${current.name}`);
    current = next;
  }
  return current;
}

describe("splitResourcePath", () => {
  it("keeps the root a path actually has", () => {
    assert.deepEqual(splitResourcePath("/data/src/app/main.go"), {
      root: "/",
      segments: ["data", "src", "app", "main.go"],
    });
    assert.deepEqual(splitResourcePath("~/notes/todo.md"), {
      root: "~",
      segments: ["notes", "todo.md"],
    });
    assert.deepEqual(splitResourcePath("C:\\Users\\alice\\main.go"), {
      root: "C:",
      segments: ["Users", "alice", "main.go"],
    });
    assert.deepEqual(splitResourcePath("\\\\fileserver\\share\\dir\\report.txt"), {
      root: "//",
      segments: ["fileserver", "share", "dir", "report.txt"],
    });
  });

  it("puts a relative path under the working-directory root", () => {
    assert.deepEqual(splitResourcePath("internal/store/store.go"), {
      root: ".",
      segments: ["internal", "store", "store.go"],
    });
  });
});

describe("buildResourceForest paths", () => {
  it("folds a repository prefix into one tree and sums counts upward", () => {
    const forest = buildResourceForest([
      path("/data/src/git.example.net/ai/adp/adp/internal/a.go", 3),
      path("/data/src/git.example.net/ai/adp/adp/internal/b.go", 2),
      path("/data/src/git.example.net/ai/adp/adp/README.md", 1),
    ]);
    assert.equal(forest.paths.roots.length, 1);
    const root = forest.paths.roots[0];
    assert.equal(root.name, "/");
    assert.equal(root.count, 6);
    assert.deepEqual(names(root.children), ["data"]);
    const repo = at(root, "data", "src", "git.example.net", "ai", "adp", "adp");
    assert.equal(repo.count, 6);
    assert.deepEqual(names(repo.children), ["internal", "README.md"]);
    const internal = at(repo, "internal");
    assert.equal(internal.count, 5);
    assert.deepEqual(names(internal.children), ["a.go", "b.go"]);
  });

  it("counts the field the caller names, so the catalogue can count requests", () => {
    const forest = buildResourceForest(
      [
        { kind: "local_path", value: "/home/a/x.go", trace_count: 7, access_count: 2 },
        { kind: "local_path", value: "/home/a/y.go", trace_count: 5, access_count: 4 },
      ],
      { countKey: "trace_count" },
    );
    assert.equal(forest.paths.roots[0].count, 12);
  });

  it("keeps each root separate", () => {
    const forest = buildResourceForest([
      path("/home/alice/a.go"),
      path("~/notes/b.md"),
      path("C:\\Users\\alice\\c.txt"),
      path("\\\\server\\share\\d.txt"),
      path("internal/store/store.go"),
    ]);
    assert.deepEqual(
      forest.paths.roots.map((root) => root.name).sort(),
      [".", "/", "//", "C:", "~"].sort(),
    );
  });

  it("treats a path that is also a directory as both", () => {
    const forest = buildResourceForest([path("/project/src", 2), path("/project/src/main.go", 3)]);
    const project = forest.paths.roots[0].children[0];
    assert.equal(project.name, "project");
    assert.equal(project.count, 5);
    const src = project.children[0];
    assert.equal(src.name, "src");
    assert.equal(src.ownItems.length, 1);
    assert.equal(src.children[0].name, "main.go");
  });
});

describe("classifyURLHost", () => {
  it("separates addresses, domains, bare hosts and unreadable authorities", () => {
    assert.equal(classifyURLHost("10.0.0.5"), "ip");
    assert.equal(classifyURLHost("127.0.0.1"), "ip");
    assert.equal(classifyURLHost("[::1]"), "ip");
    assert.equal(classifyURLHost("api.github.com"), "domain");
    assert.equal(classifyURLHost("git.in.chaitin.net"), "domain");
    assert.equal(classifyURLHost("localhost"), "host");
    assert.equal(classifyURLHost("intranet"), "host");
    assert.equal(classifyURLHost(""), "url");
  });
});

describe("buildResourceForest urls", () => {
  it("groups by category, authority and path", () => {
    const forest = buildResourceForest([
      url("https://api.github.com/repos/example/project?page=2", 4),
      url("https://api.github.com/repos/example/project", 1),
      url("http://10.0.0.5:8080/v1/health", 2),
      url("http://localhost:3080/api/traces", 1),
    ]);
    assert.deepEqual(forest.urls.roots.map((root) => root.name), ["host", "ip", "domain"]);
    const domain = forest.urls.roots[2];
    assert.equal(domain.count, 5);
    const authority = at(domain, "api.github.com");
    assert.deepEqual(names(authority.children), ["repos"]);
    const example = at(authority, "repos", "example");
    // One leaf per distinct URL; the query keeps the two apart.
    assert.deepEqual(names(example.children).sort(), ["project", "project ?page=2"]);
    assert.equal(example.children.filter((child) => child.name === "project").length, 1);
    const ip = forest.urls.roots[1];
    assert.equal(ip.children[0].name, "10.0.0.5:8080");
    const host = forest.urls.roots[0];
    assert.equal(host.children[0].name, "localhost:3080");
  });

  it("ends a URL with no path at its authority", () => {
    const forest = buildResourceForest([url("https://example.com", 3), url("https://example.com/docs", 1)]);
    const authority = at(forest.urls.roots[0], "example.com");
    assert.equal(authority.ownItems.length, 1);
    assert.equal(authority.count, 4);
    assert.deepEqual(names(authority.children), ["docs"]);
  });

  it("keeps a URL the parser cannot read as a leaf instead of dropping it", () => {
    const forest = buildResourceForest([url("https://", 1)]);
    assert.equal(forest.urls.roots[0].name, "url");
    assert.equal(forest.urls.roots[0].children[0].name, "https://");
  });
});

describe("defaultExpandedIDs", () => {
  it("opens a small tree completely", () => {
    const forest = buildResourceForest([path("/a/b/c.txt"), path("/a/d/e.txt")]);
    const expanded = defaultExpandedIDs(forest.paths);
    assert.equal(expanded.size, 4);
    assert.equal(countForestLeaves(forest.paths), 2);
  });

  it("opens only the first levels of a large tree", () => {
    const items = [];
    for (let i = 0; i < 200; i++) {
      items.push(path(`/data/src/git.example.net/ai/adp/adp/internal/pkg${i}/file.go`));
    }
    const forest = buildResourceForest(items);
    const expanded = defaultExpandedIDs(forest.paths);
    assert.ok(expanded.size > 0);
    assert.ok(expanded.size < allDirectoryIDs(forest.paths).size);
  });
});
