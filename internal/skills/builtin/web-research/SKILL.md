---
name: web-research
description: Look things up on the web - how to use a program or command, the latest version or release of something, news, prices, or anything that may have changed. Use when asked to search the web or look something up online.
allowed-tools: web_search, web_fetch
---

# Web research

1. Search first with web_search, using the user's own words. When the user names a product, company or person, put that name in double quotes, as the user wrote it. Never swap in a product or company you know for the one the user named: a name you don't recognise is likely newer than your training.
2. Check that the results are about the thing the user named. If they aren't, say so, and search again with other words, such as the name with its maker or its field. Never answer about something else.
3. Treat each snippet as a pointer to a page, not as the answer: snippets are short and often old. Pick the one or two best results and read them with web_fetch, passing a prompt that asks for the fact you need. Prefer primary sources: the project's own site or release page, and official docs. Use blogs, news sites and aggregators only when no primary source answers. For the latest version of something, read the page that lists every release with its date, such as a release history or changelog, rather than the notes for one release. Ask it to list the newest entries with their dates, then pick the latest date yourself.
4. Check dates. The system prompt gives today's date. A page or snippet from months ago may name a version or price that has since changed; say how old your source is when it matters.
5. Quote versions, numbers and dates exactly as the page writes them. Never round or guess one.
6. Cite each source by its URL.
7. When sources disagree, say so, give each answer with its source, and say which one you trust more and why.
