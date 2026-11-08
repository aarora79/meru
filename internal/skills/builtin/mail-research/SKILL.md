---
name: mail-research
description: Search the user's email inbox and calendar - emailed receipts and invoices, booking and order confirmations, appointments, flight changes and replies from people. Use when a question asks whether someone emailed the user, what an email says, what an emailed receipt or booking shows, or when an appointment, meeting or trip is, or asks to check mail or the calendar.
allowed-tools: google.search_gmail_messages, google.get_gmail_message_content, google.get_gmail_messages_content_batch, google.get_gmail_thread_content, google.get_gmail_attachment_content, google.get_events
---

# Mail research

1. Write Gmail queries, not web searches. Gmail understands these, and nothing like `site:`:
   - dates: `after:2026/11/01`, `before:2026/12/01`, `newer_than:30d`. A month or a year written as a word only matches that word.
   - a sender: `from:bookings.example.com`, or the name the user gave, such as `from:dentist`.
   - attachments: `has:attachment`, `filename:pdf`.
   - one of several words: `{receipt invoice folio}`.
2. For "last", "latest" or "recent", search by date, newest first: start with `newer_than:60d` and widen it only if nothing comes back.
3. Search for what the email is, not where it happened. A receipt says receipt, folio, invoice, order, "your stay" or "thank you for your purchase"; it often comes from a no-reply address. A place name may not appear: a hotel near a city is in another town. Never make up a sender address.
4. Read the subjects and senders of every result before you pick. The email you need may have a subject such as "We hope you enjoyed your stay". Open the ones that could answer, and open a receipt's attachment too: the amounts are often only there.
5. When the user names an event, such as a trip or a meeting, call get_events for its dates first, then search mail inside those dates.
6. Quote amounts, dates and names exactly as the email or the attachment writes them. Add up only what the receipt itemizes, and say the total is yours when the receipt doesn't give one.
7. If nothing you found answers the question, say which queries you ran, and ask the user for a sender, a date or a word from the email.
