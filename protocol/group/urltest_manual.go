package group

import (
	"context"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common"
)

// URLTestMembers runs an on-demand test of outbounds without a round of their own.
// Nested URL-test groups run their own round. Every plain node is probed at the
// target it is displayed with, the nearest selector's url or the default URL for
// a node tested directly, and at the target of each URL-test group containing it,
// so those groups can re-evaluate their selection from the fresh results.
func URLTestMembers(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound) {
	var nested []adapter.Outbound
	leaves := make(map[string]adapter.Outbound)
	targets := make(map[string]map[string]bool)
	addTarget := func(leaf adapter.Outbound, link string) {
		leaves[leaf.Tag()] = leaf
		if targets[leaf.Tag()] == nil {
			targets[leaf.Tag()] = make(map[string]bool)
		}
		targets[leaf.Tag()][link] = true
	}
	visited := make(map[string]bool)
	var walk func(outbounds []adapter.Outbound, link string)
	walk = func(outbounds []adapter.Outbound, link string) {
		for _, detour := range outbounds {
			switch member := detour.(type) {
			case *URLTest:
				if !visited[member.Tag()] {
					visited[member.Tag()] = true
					nested = append(nested, member)
				}
			case *Selector:
				if !visited[member.Tag()] {
					visited[member.Tag()] = true
					walk(resolveOutbounds(outboundManager, member.All()), member.link)
				}
			case adapter.OutboundGroup:
				if !visited[member.Tag()] {
					visited[member.Tag()] = true
					walk(resolveOutbounds(outboundManager, member.All()), link)
				}
			default:
				addTarget(member, link)
			}
		}
	}
	walk(outbounds, urltest.DefaultURL)

	affected := make(map[*URLTest]bool)
	for _, leaf := range leaves {
		for _, test := range urlTestGroupsOf(outboundManager, leaf) {
			affected[test] = true
			addTarget(leaf, test.link)
		}
	}
	byTarget := make(map[string][]adapter.Outbound)
	for tag, links := range targets {
		for link := range links {
			byTarget[link] = append(byTarget[link], leaves[tag])
		}
	}

	var wait sync.WaitGroup
	if len(nested) > 0 {
		wait.Go(func() {
			URLTestOutbounds(ctx, outboundManager, history, logger, nested, "", 0, true)
		})
	}
	for link, nodes := range byTarget {
		wait.Go(func() {
			URLTestOutbounds(ctx, outboundManager, history, logger, nodes, link, 0, true)
		})
	}
	wait.Wait()
	if ctx.Err() != nil {
		return
	}
	for test := range affected {
		test.PerformUpdateCheck()
	}
}

// urlTestGroupsOf returns the URL-test groups probing leaf at their own target. A leaf
// reached through a nested URL-test group belongs to that group only, matching historyTarget.
func urlTestGroupsOf(outboundManager adapter.OutboundManager, leaf adapter.Outbound) []*URLTest {
	var groups []*URLTest
	for _, detour := range outboundManager.Outbounds() {
		test, isURLTest := detour.(*URLTest)
		if !isURLTest || test.group == nil {
			continue
		}
		test.group.nodesAccess.RLock()
		members := test.group.outbounds
		test.group.nodesAccess.RUnlock()
		if urlTestReaches(outboundManager, members, leaf, map[string]bool{test.Tag(): true}) {
			groups = append(groups, test)
		}
	}
	return groups
}

func urlTestReaches(outboundManager adapter.OutboundManager, members []adapter.Outbound, leaf adapter.Outbound, visited map[string]bool) bool {
	for _, member := range members {
		if member == leaf {
			return true
		}
		if _, isURLTest := member.(*URLTest); isURLTest {
			continue
		}
		memberGroup, isGroup := member.(adapter.OutboundGroup)
		if !isGroup || visited[member.Tag()] {
			continue
		}
		visited[member.Tag()] = true
		if urlTestReaches(outboundManager, resolveOutbounds(outboundManager, memberGroup.All()), leaf, visited) {
			return true
		}
	}
	return false
}

func resolveOutbounds(outboundManager adapter.OutboundManager, tags []string) []adapter.Outbound {
	return common.FilterNotNil(common.Map(tags, func(tag string) adapter.Outbound {
		outbound, _ := outboundManager.Outbound(tag)
		return outbound
	}))
}
