NOWPayments MCP server
About
The NOWPayments MCP Server is a lightweight wrapper around the NOWPayments API that enables seamless interaction with cryptocurrency payments through LLM-powered interfaces such as chatbots and AI agents.

It abstracts away direct API integration, allowing you to create payments, generate invoices, and track transactions using simple tool calls.

Link:
http://mcp.nowpayments.io/mcp

Key Features
Support for a wide range of cryptocurrencies
Create hosted invoices and direct payments
Real-time payment status tracking
Minimum amount calculation for currency pairs
Simple API key–based authentication
Use Cases
Crypto payments in AI agents and chat interfaces
Automated billing and payment workflows
E-commerce checkout with crypto support
No-code / low-code payment integrations
Features
Retrieve a full list of supported cryptocurrencies
Calculate minimum payment amounts for currency pairs
Create hosted payment invoices
Create direct crypto payments
Track payment status in real time
Authentication
All methods require a NOWPayments API key.

The API key is used internally by the MCP server to authenticate requests to the NOWPayments API.

Methods
get_full_currencies
Get the list of cryptocurrencies supported by NOWPayments, including detailed metadata for each currency.

Use cases:

Display available payment options
Validate supported currencies before creating payments
get_min_amount
Get the minimum payment amount required for a specific pay-in / payout currency pair, including both:

Fixed-rate option
Standard (floating-rate) option
Use cases:

Validate payment amounts before creating a transaction
Show minimum thresholds to users
create_invoice
Create a hosted invoice and return a payment URL.

The payer is redirected to a NOWPayments-hosted payment page to complete the transaction.

Use cases:

E-commerce checkout flows
create_payment
Create a direct payment with predefined parameters.

Returns all required details for the payer to manually send funds (e.g., wallet address, amount).

Use cases:

Custom payment flows
Backend-controlled transactions
Advanced integrations
get_payment_status
Retrieve the current status and details of a payment using its payment_id.

Use cases:

Monitor payment lifecycle
Update order status
Handle confirmations and failures
How It Works
The MCP server acts as a bridge between LLM-based interfaces and the NOWPayments API.

Instead of writing API integrations manually, you can:

Ask a chatbot to create a payment
Generate invoices dynamically
Check transaction statuses in real time
All requests are securely handled using your NOWPayments API key.

Example Use Cases
Crypto payment processing in AI agents
Automated billing workflows
Chat-based checkout experiences
Internal tools for finance teams
Web3-friendly SaaS integrations
How to Start
1. Get your API key
Sign up at NOWPayments and generate your API key from the dashboard.

2. Configure the MCP Server
Provide your API key in the MCP server configuration:

3. Connect to your MCP client
Add the NOWPayments MCP server to your MCP-compatible client (e.g., ChatGPT, Claude, or other agent frameworks).

4. Start using it
Once connected, you can call methods directly through your LLM:

Examples:

“Get available cryptocurrencies” → get_full_currencies
“Create a $50 BTC invoice” → create_invoice
“Check payment status” → get_payment_status
FAQ
What is the NOWPayments MCP Server?
This tool is an MCP server for interacting with the NOWPayments API, enabling programmatic creation of crypto payments, invoices, and payment tracking through LLM-powered interfaces.

What key features does it offer?
It provides essential features for crypto payment workflows, including:

Retrieving supported cryptocurrencies
Calculating minimum payment amounts
Creating hosted invoices and direct payments
Tracking payment status in real time
Can I create invoices and payments using this tool?
Yes, the server includes dedicated methods for both hosted invoices (create_invoice) and direct payments (create_payment), allowing flexible integration depending on your use case.

What do I need to get started?
You need an active NOWPayments account and an API key.
The API key must be configured (e.g., as NOWPAYMENTS_API_KEY) and is used to authenticate all requests.

How are payment amounts handled?
Payment amounts depend on the selected cryptocurrency and currency pair.
You can use the get_min_amount method to retrieve the minimum required amount for both fixed-rate and floating-rate transactions before creating a payment.

What is the difference between fixed-rate and standard (floating-rate) payments?
Fixed-rate payments lock the exchange rate for a limited time (10 minutes), ensuring the payer sends an exact amount and the recipient receives a predictable payout. This is useful for price stability and avoiding volatility.
Standard (floating-rate) payments use the current market rate at the time the transaction is processed. The final received amount may vary depending on market fluctuations.
You can use the get_min_amount method to compare minimum amounts for both options before choosing the preferred flow.