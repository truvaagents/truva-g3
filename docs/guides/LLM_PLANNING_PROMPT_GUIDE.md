# Domain-Specific Agent Configuration Guide

Welcome! If you're building agents that handle tasks across different domains—travel booking, healthcare records, financial transactions, e-commerce, or scientific research—this guide is for you. Think of it as a senior developer walking you through how to teach your orchestrator agent about your specific domain.

## Table of Contents

- [Why This Guide Exists](#why-this-guide-exists)
- [The Problem: LLMs Don't Know Your Domain](#the-problem-llms-dont-know-your-domain)
- [The Solution: PromptConfig](#the-solution-promptconfig)
- [Quick Start: Your First Domain Configuration](#quick-start-your-first-domain-configuration)
- [Understanding the Three Layers](#understanding-the-three-layers)
- [Domain Examples](#domain-examples)
  - [Healthcare: Prior Authorization](#healthcare-agent-prior-authorization)
  - [Finance: Credit Card Approval](#financial-services-agent-credit-card-instant-approval)
  - [E-Commerce: Self-Service Returns](#e-commerce-agent-self-service-returns)
  - [Travel: Flight Disruption Rebooking](#travel-agent-flight-disruption-rebooking)
  - [Scientific: Literature Review Screening](#scientific-research-agent-literature-review-screening)
- [Type Rules: Teaching the LLM About Your Data](#type-rules-teaching-the-llm-about-your-data)
- [Custom Instructions: Domain-Specific Guidance](#custom-instructions-domain-specific-guidance)
- [Advanced: Template-Based Customization](#advanced-template-based-customization)
- [Advanced: Custom PromptBuilder](#advanced-custom-promptbuilder)
- [Environment Variable Configuration](#environment-variable-configuration)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Troubleshooting](#troubleshooting)
- [Quick Reference](#quick-reference)

---

## Why This Guide Exists

Building a multi-agent system is exciting—you wire up tools, create an orchestrator, and it works! But then you notice something: the orchestrator keeps making mistakes specific to your domain.

"Why does it keep putting coordinates in quotes? The weather API needs numbers!"

"It's sending patient IDs as integers, but they're strings with leading zeros!"

"The currency amount should be a number, not `"1500.00"`!"

This guide solves that problem. You'll learn how to configure your orchestrator with domain-specific knowledge so the LLM generates correct execution plans every time.

---

## The Problem: LLMs Don't Know Your Domain

When your orchestrator asks an LLM to create an execution plan, the LLM has to generate JSON with the right parameter types. But here's the thing: **LLMs don't inherently know that latitude should be a number, not a string**.

### What Goes Wrong

Let's say you have a weather tool that expects coordinates as numbers:

```go
// Your tool expects this:
{
    "lat": 35.6762,
    "lon": 139.6503
}

// But the LLM might generate this:
{
    "lat": "35.6762",    // String! Your tool crashes or returns errors
    "lon": "139.6503"
}
```

Or a healthcare system where patient IDs have leading zeros:

```go
// Correct (string preserves leading zero):
{"patient_id": "00123456"}

// Wrong (number loses leading zero):
{"patient_id": 123456}
```

### The Root Cause

The TruvaG3 framework is intentionally **domain-agnostic**. It doesn't hardcode rules like "latitude is always a number" because:

1. Domains are infinite (healthcare, finance, gaming, IoT, scientific research...)
2. The same field name might have different types in different contexts
3. Hardcoding rules creates maintenance nightmares

Instead, **you tell the framework about your domain**, and it teaches the LLM.

---

## The Solution: PromptConfig

The `PromptConfig` struct lets you inject domain knowledge into the LLM's planning prompt. Think of it as a briefing document you give to a new team member before they start work.

```go
config.PromptConfig = orchestration.PromptConfig{
    SystemInstructions:  "You are a travel planning assistant...", // Agent persona
    Domain:              "your-domain",      // Context hint
    AdditionalTypeRules: []TypeRule{...},    // "These fields should be these types"
    CustomInstructions:  []string{...},      // "Here's how we do things here"
}
```

When the orchestrator builds the planning prompt, it includes your configuration. The LLM sees your persona (`SystemInstructions`), type rules, and custom instructions, then generates plans that match your domain's requirements.

---

## Quick Start: Your First Domain Configuration

Let's start with the simplest possible example. You have a geocoding tool that expects coordinates as numbers.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
)

func main() {
    // Create base agent with discovery enabled
    agent := core.NewBaseAgent("my-agent")
    agent.Config.Discovery.Enabled = true
    agent.Config.Discovery.RedisURL = os.Getenv("REDIS_URL") // e.g., "localhost:6379"

    // Initialize agent (sets up discovery)
    if err := agent.Initialize(context.Background()); err != nil {
        log.Fatal(err)
    }

    // Create AI client
    aiClient, err := ai.NewClient(
        ai.WithProvider("openai"),
        ai.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
    )
    if err != nil {
        log.Fatal(err)
    }
    agent.AI = aiClient

    // Create orchestrator with domain configuration
    config := orchestration.DefaultConfig()

    // Tell the LLM about domain-specific data types beyond core rules.
    // Core rules already cover number, integer, boolean, etc.
    // Use AdditionalTypeRules for format patterns the core rules don't cover.
    config.PromptConfig = orchestration.PromptConfig{
        AdditionalTypeRules: []orchestration.TypeRule{
            {
                TypeNames:   []string{"currency_code", "from_currency", "to_currency"},
                JsonType:    "JSON strings",
                Example:     `"USD"`,
                Description: "ISO 4217 currency codes (USD, EUR, JPY, etc.)",
            },
        },
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery: agent.Discovery,  // From initialized agent
        AIClient:  aiClient,
    }

    orchestrator, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        log.Fatal(err)
    }

    // Start the orchestrator (refreshes capability catalog)
    if err := orchestrator.Start(context.Background()); err != nil {
        log.Fatal(err)
    }

    // Process a request - the LLM now knows coordinates should be numbers!
    response, err := orchestrator.ProcessRequest(
        context.Background(),
        "What's the weather in Tokyo?",
        nil, // optional metadata
    )
    if err != nil {
        log.Fatal(err)
    }

    // Use the response
    fmt.Printf("Response: %s\n", response.Response)
    fmt.Printf("Tools used: %v\n", response.AgentsInvolved)
    fmt.Printf("Confidence: %.2f\n", response.Confidence)
}
```

The LLM now knows that `lat` and `lon` should be numbers, not strings. When processing "What's the weather in Tokyo?", it will generate a plan with `"lat": 35.6762` instead of `"lat": "35.6762"`.

> **Full Example**: See [examples/agent-with-orchestration](../../examples/agent-with-orchestration/) for a production-ready implementation with HTTP handlers, telemetry, and graceful shutdown.

---

## Understanding the Three Layers

The PromptBuilder system has three layers, designed so you can start simple and add complexity only when needed:

```
┌─────────────────────────────────────────────────────────────────────┐
│  Layer 3: Custom PromptBuilder Interface                             │
│  • Full control over prompt construction                             │
│  • Use when: Compliance requirements, external services, ML models   │
│  • Complexity: High                                                  │
├─────────────────────────────────────────────────────────────────────┤
│  Layer 2: TemplatePromptBuilder                                      │
│  • Go text/template for structural changes                           │
│  • Use when: Need different prompt structure, multi-language         │
│  • Complexity: Medium                                                │
├─────────────────────────────────────────────────────────────────────┤
│  Layer 1: DefaultPromptBuilder + PromptConfig                        │
│  • Type rules + Custom instructions                                  │
│  • Use when: 90% of cases—just teaching LLM about your types         │
│  • Complexity: Low                                                   │
└─────────────────────────────────────────────────────────────────────┘
```

**Start with Layer 1.** Most applications never need more.

---

## Domain Examples

Let's walk through complete examples for different domains. Each shows a realistic agent configuration with the type rules and instructions that domain needs.

> **Built-in vs Custom Domains**: Three domains (`healthcare`, `finance`, `legal`) have built-in additions that are automatically appended to the prompt. All other domains work by relying on your `AdditionalTypeRules` and `CustomInstructions`—which is often all you need!

> **Full Reference**: These examples focus on the `PromptConfig` setup. For complete agent implementation with HTTP handlers, telemetry, and production patterns, see [examples/agent-with-orchestration](../../examples/agent-with-orchestration/).

### Healthcare Agent: Prior Authorization

**Business Process**: Electronic Prior Authorization (ePA)

Prior authorization is the process where healthcare providers must get approval from insurance payers before delivering certain medical services. Traditionally a manual, fax-based process taking 2-14 days, electronic Prior Authorization (ePA) using FHIR APIs can complete in seconds.

In 2024, CMS finalized rules requiring payers to implement FHIR-based Prior Authorization APIs by January 2027 (CMS-0057-F). The [Da Vinci Implementation Guides](https://hl7.org/fhir/us/davinci-pas/) define the standards: CRD (Coverage Requirements Discovery), DTR (Documentation Templates and Rules), and PAS (Prior Authorization Support).

**The workflow this agent handles:**
1. Provider initiates PA request for a procedure (e.g., MRI, specialty medication)
2. Agent calls CRD to check if prior auth is required for this payer/procedure
3. If required, agent calls DTR to get required documentation template
4. Agent populates template with patient clinical data from EHR
5. Agent submits PA request via PAS and returns decision (approved/pended/denied)

```go
package main

import (
    "context"
    "os"

    "github.com/truvaagents/truva-g3/ai"
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
    "github.com/truvaagents/truva-g3/telemetry"
)

type PriorAuthAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
}

func NewPriorAuthAgent() (*PriorAuthAgent, error) {
    agent := core.NewBaseAgent("prior-auth-agent")

    aiClient, err := ai.NewClient(
        ai.WithProvider("openai"),
        ai.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
        ai.WithModel("gpt-4o"),
    )
    if err != nil {
        return nil, err
    }
    agent.AI = aiClient

    return &PriorAuthAgent{BaseAgent: agent}, nil
}

func (p *PriorAuthAgent) InitializeOrchestrator(discovery core.Discovery) error {
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous
    config.SynthesisStrategy = orchestration.StrategyLLM

    // Prior Authorization agent configuration
    config.PromptConfig = orchestration.PromptConfig{
        Domain: "healthcare",  // Triggers built-in HIPAA reminders

        AdditionalTypeRules: []orchestration.TypeRule{
            {
                // Patient identifiers - MRN format varies by facility
                TypeNames:   []string{"patient_id", "mrn", "member_id", "subscriber_id"},
                JsonType:    "JSON strings",
                Example:     `"MRN-00123456"`,
                AntiPattern: `123456`,
                Description: "Patient/member identifiers - strings to preserve leading zeros and prefixes",
            },
            {
                // NPI (National Provider Identifier) - always 10 digits
                TypeNames:   []string{"npi", "provider_npi", "ordering_provider_npi", "rendering_provider_npi"},
                JsonType:    "JSON strings",
                Example:     `"1234567890"`,
                Description: "Healthcare provider NPI for PA requests",
            },
            {
                // Payer IDs - vary by clearinghouse
                TypeNames:   []string{"payer_id", "insurance_id", "plan_id"},
                JsonType:    "JSON strings",
                Example:     `"BCBS-IL-001"`,
                Description: "Payer/plan identifiers for routing PA requests",
            },
            {
                // CPT/HCPCS codes - the procedures requiring PA
                TypeNames:   []string{"cpt_code", "procedure_code", "hcpcs_code", "service_code"},
                JsonType:    "JSON strings",
                Example:     `"70553"`,  // MRI brain with contrast
                Description: "CPT/HCPCS procedure codes requiring prior authorization",
            },
            {
                // ICD-10 diagnosis codes - justification for the procedure
                TypeNames:   []string{"icd_code", "diagnosis_code", "icd10_code", "primary_diagnosis"},
                JsonType:    "JSON strings",
                Example:     `"G43.909"`,  // Migraine, unspecified
                Description: "ICD-10 diagnosis codes supporting medical necessity",
            },
            {
                // Prior auth reference numbers
                TypeNames:   []string{"auth_number", "pa_number", "reference_number", "tracking_number"},
                JsonType:    "JSON strings",
                Example:     `"PA-2025-00456789"`,
                Description: "Prior authorization tracking/reference numbers",
            },
            {
                // Quantity/units for services
                TypeNames:   []string{"quantity", "units", "visits", "days"},
                JsonType:    "JSON integers",
                Example:     `1`,
                AntiPattern: `"1"`,
                Description: "Service quantity (number of visits, days, units)",
            },
        },

        CustomInstructions: []string{
            // PA Workflow - Da Vinci CRD/DTR/PAS
            "WORKFLOW: First call CRD to check if prior auth is required for this payer+CPT combination",
            "If CRD returns 'auth-required', call DTR to get the documentation questionnaire",
            "Populate DTR questionnaire from patient's clinical data (diagnoses, labs, notes)",
            "Submit populated PA request via PAS and return the payer's decision",

            // Clinical Documentation
            "Include primary diagnosis (ICD-10) that supports medical necessity for the procedure",
            "For imaging (CT, MRI), include relevant clinical findings and prior treatments tried",
            "For specialty medications, include step therapy history if applicable",

            // Payer Requirements
            "Some payers require specific place-of-service codes - check CRD response",
            "For urgent/emergent requests, set priority='stat' to expedite review",
            "Track PA expiration dates - most authorizations valid for 60-90 days",

            // Error Handling
            "If PA is denied, return denial reason code and appeal instructions",
            "If PA is pended, return list of additional documentation requested",

            // HIPAA Compliance
            "Include minimum necessary clinical data - do not send entire medical record",
            "Log all PA transactions with timestamp, user, and outcome for audit trail",
        },
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,
        AIClient:            p.AI,
        Logger:              p.Logger,
        Telemetry:           telemetry.GetTelemetryProvider(),
        EnableErrorAnalyzer: true,
    }

    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return err
    }

    if err := orch.Start(context.Background()); err != nil {
        return err
    }

    p.orchestrator = orch
    return nil
}
```

**Example request this agent handles:**
> "Submit prior authorization for MRI brain with contrast (CPT 70553) for patient MRN-00123456 with diagnosis of chronic migraines (G43.909). The ordering provider is Dr. Smith, NPI 1234567890. Payer is Blue Cross IL."

### Financial Services Agent: Credit Card Instant Approval

**Business Process**: Credit Card Application Decisioning

When a customer applies for a credit card—online, in-app, or at a bank branch—the issuer must make an instant decision: approve, decline, or refer for manual review. This happens in under 60 seconds by orchestrating credit bureau pulls, fraud checks, identity verification, and risk scoring.

Major issuers like Capital One, Chase, and American Express achieve 70-90% instant approval rates by automating this workflow. The agent pulls data from credit bureaus (Equifax, Experian, TransUnion), runs the application through fraud models, and applies the issuer's credit policy rules.

**The workflow this agent handles:**
1. Validate applicant identity (name, SSN, address, DOB)
2. Pull credit report from one or more bureaus
3. Calculate debt-to-income ratio from stated income and credit report
4. Run fraud score check (application fraud, synthetic identity)
5. Apply credit policy rules to determine: approve (with limit/APR), decline (with reason), or refer

```go
// Required imports: context, github.com/truvaagents/truva-g3/core, github.com/truvaagents/truva-g3/orchestration

type CreditDecisionAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
}

func (c *CreditDecisionAgent) InitializeOrchestrator(discovery core.Discovery) error {
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous

    // Credit card instant approval configuration
    config.PromptConfig = orchestration.PromptConfig{
        Domain: "finance",  // Triggers built-in precision requirements

        AdditionalTypeRules: []orchestration.TypeRule{
            {
                // Credit scores - FICO ranges 300-850
                TypeNames:   []string{"credit_score", "fico_score", "vantage_score"},
                JsonType:    "JSON integers",
                Example:     `720`,
                AntiPattern: `"720"`,
                Description: "Credit scores as integers (300-850 range for FICO)",
            },
            {
                // Fraud scores - typically 0-999 or 0-100 depending on vendor
                TypeNames:   []string{"fraud_score", "identity_score", "synthetic_id_score"},
                JsonType:    "JSON integers",
                Example:     `15`,
                Description: "Fraud/risk scores from fraud detection models",
            },
            {
                // Income figures
                TypeNames:   []string{"annual_income", "stated_income", "verified_income", "monthly_income"},
                JsonType:    "JSON numbers",
                Example:     `75000.00`,
                AntiPattern: `"75000"`,
                Description: "Income amounts for DTI calculation",
            },
            {
                // Debt amounts from credit report
                TypeNames:   []string{"total_debt", "revolving_debt", "installment_debt", "mortgage_balance"},
                JsonType:    "JSON numbers",
                Example:     `12500.00`,
                Description: "Debt amounts from credit bureau report",
            },
            {
                // DTI ratio - decimal form (0.36 = 36%)
                TypeNames:   []string{"dti_ratio", "debt_to_income", "utilization_ratio"},
                JsonType:    "JSON numbers",
                Example:     `0.32`,
                Description: "Ratio values as decimals (0.32 = 32%)",
            },
            {
                // Credit limit and APR for approved applications
                TypeNames:   []string{"credit_limit", "approved_limit", "initial_limit"},
                JsonType:    "JSON numbers",
                Example:     `5000.00`,
                Description: "Credit limit in USD",
            },
            {
                // APR - annual percentage rate
                TypeNames:   []string{"apr", "interest_rate", "purchase_apr", "balance_transfer_apr"},
                JsonType:    "JSON numbers",
                Example:     `19.99`,
                Description: "APR as percentage (19.99 = 19.99%)",
            },
            {
                // SSN for identity verification - only last 4 in responses
                TypeNames:   []string{"ssn_last4"},
                JsonType:    "JSON strings",
                Example:     `"4321"`,
                Description: "Last 4 digits of SSN for identity matching",
            },
            {
                // Application and decision IDs
                TypeNames:   []string{"application_id", "decision_id", "reference_number"},
                JsonType:    "JSON strings",
                Example:     `"APP-2025-00987654"`,
                Description: "Application tracking identifiers",
            },
            {
                // Adverse action reason codes (FCRA required)
                TypeNames:   []string{"reason_code", "adverse_action_code", "decline_reason"},
                JsonType:    "JSON strings",
                Example:     `"R01"`,  // e.g., "Too many recent inquiries"
                Description: "Reason codes for decline decisions (FCRA requirement)",
            },
        },

        CustomInstructions: []string{
            // Decision Workflow
            "WORKFLOW: 1) Verify identity, 2) Pull credit, 3) Calculate DTI, 4) Score fraud risk, 5) Apply policy",
            "Decision outcomes: 'approve' (with limit/APR), 'decline' (with reason codes), or 'refer' (manual review)",
            "For approvals, calculate credit limit based on income and existing credit utilization",

            // Credit Bureau Integration
            "Pull credit from primary bureau first; use secondary only if primary unavailable",
            "Credit freeze or fraud alert on file requires identity verification before proceeding",
            "Use credit score from pulled report, not applicant-provided score",

            // Fraud Prevention
            "Fraud score > 500 (high risk): auto-decline or refer for manual review",
            "Address mismatch between application and credit report: flag for verification",
            "Multiple applications from same SSN in 30 days: check for velocity fraud",

            // Regulatory Compliance (FCRA, ECOA)
            "Declined applications MUST include adverse action reason codes",
            "Do not use age, race, gender, or zip code in credit decision (ECOA)",
            "Preserve credit inquiry record for dispute resolution",

            // Response Requirements
            "Never return full SSN - only last 4 digits",
            "Include decision_id in all responses for audit trail",
            "Log credit score and primary factors used in decision",
        },
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,
        AIClient:            c.AI,
        Logger:              c.Logger,
        EnableErrorAnalyzer: true,
    }

    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return err
    }

    return orch.Start(context.Background())
}
```

**Example request this agent handles:**
> "Process credit card application for John Smith, SSN ending 4321, DOB 1985-03-15, stated annual income $85,000. Application ID APP-2025-00987654."

### E-Commerce Agent: Self-Service Returns

**Business Process**: Return Merchandise Authorization (RMA)

Returns are a fact of e-commerce life—the average return rate is 16-20% for online purchases, and 30%+ during holiday seasons. Self-service returns portals (like those from Loop, AfterShip, Narvar, and ReturnLogic) let customers initiate returns without contacting support, reducing support costs by 60-70%.

The key to modern returns is offering customers options: refund to original payment, store credit (often with bonus incentives), or exchange for a different size/color. Exchange-first strategies can recover 30-40% of would-be refunds.

**The workflow this agent handles:**
1. Customer looks up order by order ID or email
2. Customer selects items to return and provides reason
3. Agent checks return eligibility (within policy window, item condition)
4. Agent offers resolution options: refund, store credit, or exchange
5. Agent generates return shipping label (prepaid or customer-paid based on reason)
6. Agent creates RMA and provides QR code/tracking info

```go
// Required imports: context, github.com/truvaagents/truva-g3/core, github.com/truvaagents/truva-g3/orchestration

type ReturnsAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
}

func (r *ReturnsAgent) InitializeOrchestrator(discovery core.Discovery) error {
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous

    // Self-service returns configuration
    config.PromptConfig = orchestration.PromptConfig{
        Domain: "retail",

        AdditionalTypeRules: []orchestration.TypeRule{
            {
                // Order identifiers
                TypeNames:   []string{"order_id", "order_number"},
                JsonType:    "JSON strings",
                Example:     `"ORD-2025-00456789"`,
                Description: "Order identifiers for lookup",
            },
            {
                // RMA (Return Merchandise Authorization) numbers
                TypeNames:   []string{"rma_number", "return_id", "rma_id"},
                JsonType:    "JSON strings",
                Example:     `"RMA-2025-00123456"`,
                Description: "Return authorization identifiers",
            },
            {
                // Product identifiers
                TypeNames:   []string{"sku", "product_id", "item_id", "variant_id"},
                JsonType:    "JSON strings",
                Example:     `"SKU-SHOE-BLK-10"`,
                Description: "Product/variant SKUs for return items",
            },
            {
                // Return quantities
                TypeNames:   []string{"return_quantity", "quantity"},
                JsonType:    "JSON integers",
                Example:     `1`,
                AntiPattern: `"1"`,
                Description: "Quantity of items being returned",
            },
            {
                // Monetary values (refund amounts, store credit)
                TypeNames:   []string{"refund_amount", "store_credit_amount", "original_price", "restocking_fee"},
                JsonType:    "JSON numbers",
                Example:     `79.99`,
                AntiPattern: `"79.99"`,
                Description: "Monetary amounts in order currency",
            },
            {
                // Store credit bonus percentage
                TypeNames:   []string{"bonus_percent", "credit_bonus"},
                JsonType:    "JSON numbers",
                Example:     `10.0`,
                Description: "Bonus percentage for store credit option (10.0 = 10% extra)",
            },
            {
                // Return reason codes
                TypeNames:   []string{"return_reason", "reason_code"},
                JsonType:    "JSON strings",
                Example:     `"WRONG_SIZE"`,
                Description: "Return reason code (WRONG_SIZE, DEFECTIVE, NOT_AS_DESCRIBED, CHANGED_MIND)",
            },
            {
                // Tracking and shipping labels
                TypeNames:   []string{"tracking_number", "label_id", "shipment_id"},
                JsonType:    "JSON strings",
                Example:     `"1Z999AA10123456784"`,
                Description: "Return shipment tracking/label identifiers",
            },
            {
                // Days since order (for return window calculation)
                TypeNames:   []string{"days_since_delivery", "days_since_order"},
                JsonType:    "JSON integers",
                Example:     `15`,
                Description: "Days elapsed for return window eligibility",
            },
            {
                // Customer identifiers
                TypeNames:   []string{"customer_id", "customer_email"},
                JsonType:    "JSON strings",
                Example:     `"customer@example.com"`,
                Description: "Customer identifier for order lookup",
            },
        },

        CustomInstructions: []string{
            // Return Eligibility
            "Check return window: standard items 30 days, final sale items non-returnable",
            "Items must be unworn/unused with tags attached (except for defective items)",
            "Verify item was actually purchased in the order being referenced",

            // Resolution Options - present in this order
            "ALWAYS offer exchange first if same item available in different size/color",
            "If exchange not possible, offer store credit with 10% bonus as primary option",
            "Refund to original payment as final option (no bonus)",

            // Shipping Label Logic
            "Defective or wrong item shipped: provide FREE prepaid return label",
            "Customer changed mind or wrong size ordered: deduct $7.95 from refund OR free if store credit",
            "Generate QR code for label-free drop-off at UPS/FedEx when available",

            // RMA Creation
            "Create RMA with unique identifier and 14-day expiration for return shipment",
            "Include packing instructions: original packaging preferred, or secure box",
            "Send confirmation email with RMA number, QR code, and drop-off locations",

            // Refund Timing
            "Refunds processed within 5-7 business days after warehouse receives return",
            "Store credit issued immediately upon RMA creation (before item received)",

            // Fraud Prevention
            "Flag customers with >3 returns in 90 days for review",
            "High-value items (>$200) require photo verification before approving return",
        },
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery: discovery,
        AIClient:  r.AI,
        Logger:    r.Logger,
    }

    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return err
    }

    return orch.Start(context.Background())
}
```

**Example request this agent handles:**
> "I want to return the black running shoes (size 10) from order ORD-2025-00456789. They're too small—I need size 11."

### Travel Agent: Flight Disruption Rebooking

**Business Process**: IRROPS (Irregular Operations) Reaccommodation

When flights are cancelled or significantly delayed due to weather, mechanical issues, or crew problems, airlines must rebook affected passengers. This is called IRROPS (Irregular Operations). Airlines spend an estimated $60B annually on IRROPS, and 67-72% of passengers prefer self-service rebooking over waiting in line or on hold.

The challenge: during a major disruption (e.g., winter storm), thousands of passengers need rebooking simultaneously. Automated rebooking agents can process reaccommodations in seconds, prioritizing by elite status, connection risk, and seat availability.

**The workflow this agent handles:**
1. Detect passenger is affected by cancelled/delayed flight (via PNR lookup)
2. Identify passenger's final destination and any connecting flights
3. Search alternative routing options (same airline, partner airlines, other alliances)
4. Present options ranked by: arrival time, number of stops, upgrade availability
5. Execute rebooking: cancel old segments, book new segments, preserve seat/meal preferences
6. Issue updated itinerary with new e-ticket numbers

```go
// Required imports: context, time, github.com/truvaagents/truva-g3/core, github.com/truvaagents/truva-g3/orchestration, github.com/truvaagents/truva-g3/telemetry

type DisruptionAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
}

func (d *DisruptionAgent) InitializeOrchestrator(discovery core.Discovery) error {
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous
    config.SynthesisStrategy = orchestration.StrategyLLM

    // GDS APIs can be slow during IRROPS - increase timeouts
    config.ExecutionOptions.TotalTimeout = 5 * time.Minute
    config.ExecutionOptions.StepTimeout = 120 * time.Second

    // IRROPS rebooking configuration
    config.PromptConfig = orchestration.PromptConfig{
        Domain: "travel",

        AdditionalTypeRules: []orchestration.TypeRule{
            {
                // PNR/Record Locator - always 6 alphanumeric
                TypeNames:   []string{"pnr", "record_locator", "confirmation_code"},
                JsonType:    "JSON strings",
                Example:     `"ABC123"`,
                Description: "PNR - 6-character alphanumeric booking identifier",
            },
            {
                // IATA airport codes - 3 letters
                TypeNames:   []string{"origin", "destination", "connection_airport", "airport_code"},
                JsonType:    "JSON strings",
                Example:     `"ORD"`,
                Description: "IATA 3-letter airport codes",
            },
            {
                // Flight numbers
                TypeNames:   []string{"flight_number", "cancelled_flight", "new_flight"},
                JsonType:    "JSON strings",
                Example:     `"UA1234"`,
                Description: "Flight number (carrier code + number)",
            },
            {
                // Airline codes
                TypeNames:   []string{"carrier_code", "operating_carrier", "marketing_carrier"},
                JsonType:    "JSON strings",
                Example:     `"UA"`,
                Description: "IATA 2-letter airline codes",
            },
            {
                // Disruption codes
                TypeNames:   []string{"disruption_code", "irrops_code", "delay_code"},
                JsonType:    "JSON strings",
                Example:     `"WX"`,  // Weather
                Description: "IRROPS reason code (WX=weather, MX=mechanical, CR=crew)",
            },
            {
                // Delay minutes
                TypeNames:   []string{"delay_minutes", "connection_time_minutes", "min_connection_time"},
                JsonType:    "JSON integers",
                Example:     `180`,
                Description: "Time duration in minutes",
            },
            {
                // Elite status tier
                TypeNames:   []string{"elite_status", "frequent_flyer_tier"},
                JsonType:    "JSON strings",
                Example:     `"1K"`,  // United's top tier
                Description: "Passenger elite status tier for priority rebooking",
            },
            {
                // Passenger counts
                TypeNames:   []string{"passenger_count", "travelers"},
                JsonType:    "JSON integers",
                Example:     `2`,
                Description: "Number of passengers in PNR",
            },
            {
                // Cabin class
                TypeNames:   []string{"cabin_class", "original_cabin", "new_cabin"},
                JsonType:    "JSON strings",
                Example:     `"Y"`,  // Economy
                Description: "Cabin class (F=first, J=business, W=premium economy, Y=economy)",
            },
            {
                // E-ticket numbers
                TypeNames:   []string{"ticket_number", "eticket"},
                JsonType:    "JSON strings",
                Example:     `"0167890123456"`,
                Description: "13-digit e-ticket number",
            },
            {
                // Fare difference for upgrades
                TypeNames:   []string{"fare_difference", "upgrade_cost", "change_fee"},
                JsonType:    "JSON numbers",
                Example:     `0.00`,  // Usually waived during IRROPS
                Description: "Cost difference (typically $0 for IRROPS rebooks)",
            },
        },

        CustomInstructions: []string{
            // IRROPS Rebooking Priority
            "Rebook passengers in priority order: unaccompanied minors, passengers with connections, elite status, then by original booking time",
            "For connecting itineraries, ensure minimum connection time (MCT) is met at hub airports",
            "If original flight delayed <4 hours, offer choice to keep or rebook",

            // Routing Options
            "Search same airline first, then codeshare partners, then alliance partners",
            "Offer up to 3 alternative routings, ranked by earliest arrival at final destination",
            "For same-day options, include nearby alternate airports (e.g., EWR for LGA)",

            // Cabin and Seat Handling
            "Preserve original cabin class; if unavailable, offer: 1) next flight in lower cabin, 2) next day same cabin",
            "If upgrading to higher cabin due to availability, no additional charge during IRROPS",
            "Attempt to preserve seat preferences (window/aisle) and frequent flyer meal requests",

            // Change Fee Waiver
            "IRROPS rebookings have $0 change fee - do not charge fare difference for same cabin",
            "If passenger chooses earlier premium cabin flight, collect fare difference",

            // Communication
            "Send rebooking confirmation via email AND SMS with new itinerary",
            "Include: new flight numbers, departure times (local), gate info if available",
            "For overnight delays, include hotel voucher information if eligible",

            // Documentation
            "Log disruption_code, original_flight, new_flight, and rebooking_timestamp for DOT reporting",
            "Preserve original ticket number association for refund eligibility",
        },
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,
        AIClient:            d.AI,
        Logger:              d.Logger,
        Telemetry:           telemetry.GetTelemetryProvider(),
        EnableErrorAnalyzer: true,
    }

    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return err
    }

    return orch.Start(context.Background())
}
```

**Example request this agent handles:**
> "Flight UA1234 ORD→SFO is cancelled due to weather. Rebook PNR ABC123 (2 passengers, 1K elite status) to arrive San Francisco today."

### Scientific Research Agent: Literature Review Screening

**Business Process**: PRISMA Systematic Literature Review Screening

Systematic reviews are the gold standard for evidence-based research, but the screening phase is brutally time-consuming. Researchers must screen thousands of paper titles and abstracts to find relevant studies. The [PRISMA 2020 guidelines](http://www.prisma-statement.org/) standardize this process, and AI-assisted screening can reduce workload by 50-70% while maintaining accuracy.

The screening process uses PICO criteria: **P**opulation, **I**ntervention, **C**omparison, **O**utcome. Each paper is classified as Include, Exclude, or Maybe (needs full-text review). Tools like Rayyan, Covidence, and ASReview have popularized ML-assisted screening.

**The workflow this agent handles:**
1. Accept search results from PubMed, Scopus, Web of Science (titles + abstracts)
2. Apply PICO inclusion/exclusion criteria to each paper
3. Classify as: Include (meets all criteria), Exclude (fails criteria), Maybe (unclear from abstract)
4. Provide reasoning for each decision (required for PRISMA documentation)
5. Generate PRISMA flow diagram counts (identified → screened → eligible → included)

```go
// Required imports: context, github.com/truvaagents/truva-g3/core, github.com/truvaagents/truva-g3/orchestration

type LitReviewAgent struct {
    *core.BaseAgent
    orchestrator *orchestration.AIOrchestrator
}

func (l *LitReviewAgent) InitializeOrchestrator(discovery core.Discovery) error {
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous

    // PRISMA literature screening configuration
    config.PromptConfig = orchestration.PromptConfig{
        Domain: "scientific",

        AdditionalTypeRules: []orchestration.TypeRule{
            {
                // PubMed IDs
                TypeNames:   []string{"pmid", "pubmed_id"},
                JsonType:    "JSON strings",
                Example:     `"38123456"`,
                Description: "PubMed identifier (8 digits)",
            },
            {
                // DOIs
                TypeNames:   []string{"doi"},
                JsonType:    "JSON strings",
                Example:     `"10.1001/jama.2024.12345"`,
                Description: "Digital Object Identifier for publication",
            },
            {
                // Screening decision
                TypeNames:   []string{"decision", "screening_decision"},
                JsonType:    "JSON strings",
                Example:     `"include"`,
                Description: "Screening decision: 'include', 'exclude', or 'maybe'",
            },
            {
                // Exclusion reason codes
                TypeNames:   []string{"exclusion_reason", "exclude_reason"},
                JsonType:    "JSON strings",
                Example:     `"WRONG_POPULATION"`,
                Description: "Reason code: WRONG_POPULATION, WRONG_INTERVENTION, WRONG_OUTCOME, WRONG_STUDY_TYPE, NOT_ENGLISH, DUPLICATE",
            },
            {
                // Confidence score for ML-assisted screening
                TypeNames:   []string{"confidence_score", "relevance_score"},
                JsonType:    "JSON numbers",
                Example:     `0.85`,
                Description: "ML confidence score (0.0-1.0) for screening decision",
            },
            {
                // Publication year
                TypeNames:   []string{"publication_year", "pub_year"},
                JsonType:    "JSON integers",
                Example:     `2024`,
                Description: "Year of publication for date filtering",
            },
            {
                // Sample sizes from abstracts
                TypeNames:   []string{"sample_size", "n_participants", "n_studies"},
                JsonType:    "JSON integers",
                Example:     `1500`,
                Description: "Sample size mentioned in abstract",
            },
            {
                // PRISMA flow counts
                TypeNames:   []string{"records_identified", "records_screened", "records_excluded", "reports_assessed", "studies_included"},
                JsonType:    "JSON integers",
                Example:     `3847`,
                Description: "PRISMA flow diagram counts",
            },
            {
                // Duplicate detection
                TypeNames:   []string{"is_duplicate", "duplicate_of"},
                JsonType:    "JSON strings",
                Example:     `"38123456"`,
                Description: "PMID of original if this is a duplicate",
            },
        },

        CustomInstructions: []string{
            // PICO Criteria Application
            "Apply PICO criteria strictly: Population, Intervention, Comparison, Outcome",
            "If abstract doesn't mention the specific intervention, mark as EXCLUDE with reason WRONG_INTERVENTION",
            "If population is clearly outside scope (e.g., animal study when looking for human), mark as EXCLUDE",

            // Screening Decisions
            "INCLUDE: Abstract clearly meets all PICO criteria",
            "EXCLUDE: Abstract clearly fails one or more PICO criteria - provide specific reason code",
            "MAYBE: Criteria unclear from abstract alone - needs full-text review",

            // Study Type Filtering
            "For meta-analyses: include only RCTs, exclude case reports, editorials, and narrative reviews",
            "If study type not clear from abstract, mark as MAYBE for full-text assessment",

            // Documentation for PRISMA
            "Provide reasoning for each decision - required for PRISMA audit trail",
            "Track exclusion reasons for PRISMA flow diagram (n excluded at screening with reasons)",

            // Handling Edge Cases
            "Conference abstracts: mark as MAYBE unless explicitly excluded by protocol",
            "Preprints: include in screening but flag as 'preprint' for quality assessment",
            "Non-English abstracts: mark as EXCLUDE with reason NOT_ENGLISH unless bilingual search specified",

            // Duplicate Detection
            "Flag potential duplicates based on matching title AND authors",
            "For duplicate pairs, keep the version with PMID (prefer PubMed over other databases)",

            // Quality Indicators
            "Note if abstract reports: sample size, study design (RCT, cohort, etc.), primary outcome",
            "Flag studies mentioning 'pilot' or 'feasibility' for sensitivity analysis consideration",
        },
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery: discovery,
        AIClient:  l.AI,
        Logger:    l.Logger,
    }

    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return err
    }

    return orch.Start(context.Background())
}
```

**Example request this agent handles:**
> "Screen this batch of 50 PubMed abstracts for our systematic review on 'effectiveness of cognitive behavioral therapy for adult insomnia'. PICO: Adults 18+ with primary insomnia (P), CBT-I intervention (I), compared to waitlist or pharmacotherapy (C), sleep quality outcomes (O). Exclude animal studies and non-English."

---

## Type Rules: Teaching the LLM About Your Data

Type rules are the core mechanism for teaching the LLM about your domain's data types. Let's break down the structure:

```go
type TypeRule struct {
    TypeNames   []string // Field names this rule applies to
    JsonType    string   // What JSON type to use
    Example     string   // Show the correct format
    AntiPattern string   // Show what NOT to do (optional but helpful)
    Description string   // Explain why (optional)
}
```

### How Type Rules Work

When you configure type rules, they get included in the LLM's planning prompt like this:

```
TYPE FORMATTING RULES:
- For fields named latitude, lat, longitude, lon: Use JSON numbers (e.g., 35.6762)
  NOT strings like "35.6762"
  These are geographic coordinates for weather and location lookups.

- For fields named currency_code, from_currency, to_currency: Use JSON strings (e.g., "USD")
  These are ISO 4217 currency codes.
```

The LLM reads these rules and applies them when generating execution plans.

### Best Practices for Type Rules

| Practice | Example | Why |
|----------|---------|-----|
| Include `AntiPattern` | `AntiPattern: \`"35.6762"\`` | Shows exactly what to avoid |
| Use multiple `TypeNames` | `["lat", "latitude"]` | Catches variations |
| Be specific in `Description` | "Geographic coordinates for API calls" | Context helps LLM |
| Group related types | Coordinates, currencies, IDs | Easier to maintain |

### Common Type Rule Patterns

**Numeric IDs that should stay strings:**
```go
{
    TypeNames:   []string{"patient_id", "account_number", "order_id"},
    JsonType:    "JSON strings",
    Example:     `"00123456"`,
    AntiPattern: `123456`,
    Description: "Identifiers with leading zeros or special characters",
}
```

**Amounts and measurements:**
```go
{
    TypeNames:   []string{"amount", "price", "temperature", "weight"},
    JsonType:    "JSON numbers",
    Example:     `123.45`,
    AntiPattern: `"123.45"`,
    Description: "Numeric values for calculations",
}
```

**Counts and quantities:**
```go
{
    TypeNames:   []string{"quantity", "count", "passengers"},
    JsonType:    "JSON integers",
    Example:     `5`,
    AntiPattern: `"5"`,
    Description: "Whole number counts",
}
```

---

## Custom Instructions: Domain-Specific Guidance

While type rules handle data formatting, custom instructions provide higher-level guidance about how to approach tasks in your domain.

```go
CustomInstructions: []string{
    "Always verify identity before accessing sensitive records",
    "Prefer parallel execution when steps are independent",
    "Include audit information in all responses",
}
```

### What Custom Instructions Are Good For

| Use Case | Example Instruction |
|----------|---------------------|
| **Workflow guidance** | "For weather queries, geocode first, then fetch weather" |
| **Compliance requirements** | "Never include PHI in plan metadata" |
| **Performance hints** | "Prefer parallel execution when steps are independent" |
| **Business rules** | "For amounts over $10,000, flag for compliance review" |
| **Error handling** | "When uncertain, ask for verification rather than assuming" |

### Writing Effective Instructions

**Be specific:**
```go
// Good - specific action
"For currency conversion, extract the destination country's currency code"

// Less helpful - too vague
"Handle currencies properly"
```

**Be actionable:**
```go
// Good - tells LLM what to do
"Always check inventory before confirming orders"

// Less helpful - states a fact
"Inventory accuracy is important"
```

---

## Advanced: Template-Based Customization

If you need to change the structure of the prompt itself (not just add rules), use Layer 2: templates. This is ideal when the prompt **structure** varies (compliance sections, language, tone) but the **logic** remains the same.

### When to Use Templates

| Scenario | Why Layer 2 |
|----------|-------------|
| **Multi-region compliance** | GDPR (EU), CCPA (California), LGPD (Brazil) require different privacy language |
| **Multi-language support** | Prompts in English, Spanish, German, Japanese, etc. |
| **A/B testing prompts** | Test different prompt structures without code changes |
| **Tenant-specific customization** | Enterprise customers want branded/customized prompts |
| **Regulatory formatting** | Specific required sections for healthcare, finance, etc. |

### When NOT to Use Templates (Use Layer 3 Instead)

- Need to call external services (audit logs, vector DB)
- Need to execute custom code logic (PII redaction, encryption)
- Need runtime decisions based on complex conditions

### Template Variables

The `TemplateData` struct ([template_prompt_builder.go:43-54](../../orchestration/template_prompt_builder.go#L43-L54)) provides these variables:

| Variable | Type | Content |
|----------|------|---------|
| `{{.CapabilityInfo}}` | `string` | Formatted list of available tools/agents from CapabilityProvider |
| `{{.Request}}` | `string` | The user's natural language request |
| `{{.TypeRules}}` | `string` | Pre-formatted string combining default rules + your `AdditionalTypeRules` |
| `{{.CustomInstructions}}` | `string` | Pre-formatted numbered list from your `CustomInstructions` array |
| `{{.Domain}}` | `string` | The configured domain string (from PromptConfig.Domain) |
| `{{.Metadata}}` | `map[string]interface{}` | Request metadata map (currently nil, use context instead) |
| `{{.ConcreteExample}}` | `string` | Concrete few-shot JSON plan example with realistic agents and template references |
| `{{.IterativePlanningInstructions}}` | `string` | Budget-aware iterative planning instructions (empty if disabled) |
| `{{.PhaseBudget}}` | `string` | Phase budget information for iterative planning |
| `{{.SystemInstructions}}` | `string` | The configured SystemInstructions from PromptConfig |

> **Iterative planning and prompts:** When `IterativePlanConfig` is enabled, the prompt builder embeds budget-aware instructions via `{{.IterativePlanningInstructions}}` (generated by `BuildIterativePlanningInstructions()`) and phase budget context via `{{.PhaseBudget}}`. During Phase 2+ continuation planning, the tiered capability provider uses context-aware tool re-selection — building a continuation selection prompt that incorporates prior tools used, the planner's continuation note, and a compact result summary. This tiered approach (discover in Phase 1, act in Phase 2+) allows the LLM to progressively refine its tool selection as new information becomes available. See [ARCHITECTURE.md § Multi-Phase Iterative Planning](../../orchestration/ARCHITECTURE.md) for the full flow.

### Detailed Example: Multi-Region Privacy-Compliant Support Agent

**Business Process**: Global Customer Data Access Compliance

When customers contact support requesting access to their data, the agent must comply with regional privacy regulations. GDPR (EU) requires specific language about data subject rights and data minimization. CCPA (California) requires disclosure of data selling practices. Other regions have different requirements.

**Why Layer 2**: The orchestration logic (routing to tools, JSON plan generation) is identical across regions. Only the compliance language and prompt structure differ. Templates let you:
1. Deploy region-specific ConfigMaps without code changes
2. Update compliance language when regulations change
3. A/B test different compliance phrasings
4. Let legal teams review/approve template text

**The workflow this agent handles:**
1. Customer requests data access (view, export, or delete their data)
2. Agent verifies customer identity via authentication tools
3. Agent queries data inventory to locate customer's data
4. Agent formats response according to regional compliance requirements
5. Agent logs the data access request for audit trail

#### Step 1: Create Region-Specific Templates

**EU Template (GDPR)** - `/config/templates/eu-gdpr.tmpl`:

```gotemplate
{{/* EU GDPR-Compliant Orchestration Prompt */}}
{{/* Last reviewed by Legal: 2025-01-15 */}}
{{/* Regulation: GDPR Articles 12-23 (Data Subject Rights) */}}

You are a customer data access orchestrator operating under GDPR (General Data Protection Regulation) requirements.

GDPR COMPLIANCE REQUIREMENTS:
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
• Data Minimization (Art. 5): Only access data strictly necessary for the request
• Purpose Limitation (Art. 5): Data accessed only for the stated purpose
• Right of Access (Art. 15): Customer has right to obtain copy of their data
• Right to Erasure (Art. 17): Customer can request deletion ("right to be forgotten")
• Right to Portability (Art. 20): Provide data in machine-readable format (JSON/CSV)
• Response Deadline: Complete within one calendar month (Art. 12), extendable by two further months for complex requests

DATA SUBJECT RIGHTS - RESPONSE REQUIREMENTS:
• For ACCESS requests: Return all personal data categories with processing purposes
• For ERASURE requests: Confirm deletion and notify any third parties who received the data
• For PORTABILITY requests: Export in structured, commonly used, machine-readable format

AVAILABLE CAPABILITIES:
{{.CapabilityInfo}}

{{if .TypeRules}}
TYPE FORMATTING RULES:
{{.TypeRules}}
{{end}}

{{if .CustomInstructions}}
ADDITIONAL INSTRUCTIONS:
{{.CustomInstructions}}
{{end}}

CUSTOMER REQUEST:
{{.Request}}

Generate a JSON execution plan. Each step MUST include:
1. Justification for why this data access is necessary (data minimization)
2. The specific data categories being accessed
3. Audit metadata for GDPR compliance logging

{{.ConcreteExample}}

CRITICAL: Log all data access for Article 30 (Records of Processing Activities).
Include "gdpr_lawful_basis": "data_subject_request" in every step's metadata.

Response (JSON only):
```

**US California Template (CCPA)** - `/config/templates/us-ccpa.tmpl`:

```gotemplate
{{/* California CCPA-Compliant Orchestration Prompt */}}
{{/* Last reviewed by Legal: 2025-01-15 */}}
{{/* Regulation: CCPA/CPRA (California Consumer Privacy Act) */}}

You are a customer data access orchestrator operating under CCPA (California Consumer Privacy Act) requirements.

CCPA COMPLIANCE REQUIREMENTS:
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
• Right to Know (§1798.100): Disclose what personal information is collected
• Right to Delete (§1798.105): Delete consumer's personal information upon request
• Right to Opt-Out (§1798.120): Consumer can opt out of sale/sharing of personal info
• Right to Non-Discrimination (§1798.125): Cannot discriminate for exercising rights
• Response Deadline: 45 days (extendable to 90 with notice)

CCPA CATEGORIES TO DISCLOSE:
• Categories of personal information collected
• Sources of personal information
• Business purpose for collecting/selling
• Categories of third parties with whom info is shared
• Specific pieces of personal information collected

AVAILABLE CAPABILITIES:
{{.CapabilityInfo}}

{{if .TypeRules}}
TYPE FORMATTING RULES:
{{.TypeRules}}
{{end}}

{{if .CustomInstructions}}
ADDITIONAL INSTRUCTIONS:
{{.CustomInstructions}}
{{end}}

CONSUMER REQUEST:
{{.Request}}

Generate a JSON execution plan. For data access requests, include:
1. All categories of personal information (CCPA-defined categories)
2. Whether data has been sold/shared in past 12 months
3. Third parties who received the data

{{.ConcreteExample}}

Include "ccpa_request_type": "know|delete|opt_out" in every step's metadata.

Response (JSON only):
```

**Standard Template (Non-regulated regions)** - `/config/templates/standard.tmpl`:

```gotemplate
{{/* Standard Privacy-Respecting Orchestration Prompt */}}

You are a customer support orchestrator. Help customers access and manage their account data.

PRIVACY BEST PRACTICES:
• Access only data necessary to fulfill the request
• Verify customer identity before accessing personal data
• Log all data access for audit purposes

AVAILABLE CAPABILITIES:
{{.CapabilityInfo}}

{{if .TypeRules}}
TYPE FORMATTING RULES:
{{.TypeRules}}
{{end}}

{{if .CustomInstructions}}
ADDITIONAL INSTRUCTIONS:
{{.CustomInstructions}}
{{end}}

CUSTOMER REQUEST:
{{.Request}}

Generate a JSON execution plan:
{{.ConcreteExample}}

Response (JSON only):
```

#### Step 2: Kubernetes ConfigMaps

Deploy templates as ConfigMaps so legal/compliance teams can update them without code deployments.

**Create ConfigMap from template file** (recommended):

```bash
# Create ConfigMap from the template file in Step 1
kubectl create configmap orchestrator-template-eu \
  --from-file=prompt.tmpl=./templates/eu-gdpr.tmpl \
  -n truvag3

# Add annotations for tracking
kubectl annotate configmap orchestrator-template-eu \
  legal-review-date="2025-01-15" \
  regulation="GDPR 2016/679" \
  -n truvag3
```

**Deployment referencing the ConfigMap:**

```yaml
# deployment-eu.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: data-access-agent-eu
  namespace: truvag3
spec:
  selector:
    matchLabels:
      app: data-access-agent
      region: eu
  template:
    metadata:
      labels:
        app: data-access-agent
        region: eu
    spec:
      containers:
      - name: agent
        image: data-access-agent:latest
        env:
        - name: TRUVAG3_PROMPT_TEMPLATE_FILE
          value: /config/prompt.tmpl
        - name: TRUVAG3_PROMPT_DOMAIN
          value: gdpr
        volumeMounts:
        - name: template
          mountPath: /config
          readOnly: true
      volumes:
      - name: template
        configMap:
          name: orchestrator-template-eu
```

**Multi-region**: Create separate ConfigMaps and Deployments per region. Route traffic via Service selectors or Ingress geo-routing.

#### Step 3: Go Code to Wire It Up

```go
func createGDPROrchestrator(discovery core.Discovery, aiClient core.AIClient) (*orchestration.AIOrchestrator, error) {
    config := orchestration.DefaultConfig()

    // Layer 2: Configure template-based prompt building
    //
    // TEMPLATE VARIABLE SOURCES:
    // ─────────────────────────────────────────────────────────────────
    // {{.CapabilityInfo}}     ← From discovery (CapabilityProvider)
    // {{.Request}}            ← From orchestrator.ProcessRequest(ctx, REQUEST, nil)
    // {{.Domain}}             ← From config.PromptConfig.Domain (below)
    // {{.TypeRules}}          ← Pre-formatted string: default rules + AdditionalTypeRules
    //                           (see "What TypeRules Looks Like" below)
    // {{.CustomInstructions}} ← Pre-formatted string from CustomInstructions array
    // {{.ConcreteExample}}    ← Few-shot example from buildConcreteExample()
    // {{.Metadata}}           ← Currently nil (use context.Context instead)
    // ─────────────────────────────────────────────────────────────────

    config.PromptConfig = orchestration.PromptConfig{
        TemplateFile: "/config/prompt.tmpl",  // ConfigMap mount path
        Domain:       "gdpr",                  // → {{.Domain}}

        AdditionalTypeRules: []orchestration.TypeRule{  // → {{.TypeRules}}
            {
                TypeNames: []string{"customer_id"},
                JsonType:  "JSON strings",
                Example:   `"cust_abc123"`,
            },
        },

        CustomInstructions: []string{  // → {{.CustomInstructions}}
            "Verify customer identity before accessing personal data",
        },
    }

    deps := orchestration.OrchestratorDependencies{
        Discovery: discovery,  // Provides {{.CapabilityInfo}}
        AIClient:  aiClient,
    }

    return orchestration.CreateOrchestrator(config, deps)
}
```

**What `{{.TypeRules}}` Looks Like in the Rendered Prompt:**

The `TypeRules` variable is a pre-formatted string generated by `DefaultPromptBuilder.buildTypeRulesSection()`. It combines the framework's default type rules with your `AdditionalTypeRules`:

```
- Parameters with type "string" MUST be JSON strings (e.g., "text value")
- Parameters with type "number" or "float64" or "float32" or "float" MUST be JSON numbers (e.g., 35.6897), NOT strings (e.g., "35.6897")
- Parameters with type "integer" or "int" or "int64" or "int32" MUST be JSON integers (e.g., 10), NOT strings (e.g., "10")
- Parameters with type "boolean" or "bool" MUST be JSON booleans (e.g., true), NOT strings (e.g., "true")
- Parameters with type "array" or "[]string" or "[]int" or "[]float64" or "list" MUST be JSON arrays (e.g., ["item1", "item2"])
- Parameters with type "object" or "map" or "struct" or "map[string]interface{}" MUST be JSON objects (e.g., {"key": "value", "count": 5})
- Parameters with type "customer_id" MUST be JSON strings (e.g., "cust_abc123")
```

The last line comes from your `AdditionalTypeRules`. This is why you only need to add domain-specific rules—the common types are handled automatically.

**Using Environment Variables (Kubernetes-friendly):**

Instead of hardcoding paths, use `LoadFromEnv()` to read from environment variables:

```go
config := orchestration.DefaultConfig()

// Load template path from TRUVAG3_PROMPT_TEMPLATE_FILE env var
// Load domain from TRUVAG3_PROMPT_DOMAIN env var
if err := config.PromptConfig.LoadFromEnv(); err != nil {
    return nil, fmt.Errorf("failed to load prompt config: %w", err)
}

// Add type rules programmatically (these can't come from env vars)
config.PromptConfig.AdditionalTypeRules = append(
    config.PromptConfig.AdditionalTypeRules,
    orchestration.TypeRule{
        TypeNames: []string{"customer_id"},
        JsonType:  "JSON strings",
        Example:   `"cust_abc123"`,
    },
)
```

**Alternative: Inline template** (no external file needed)

```go
config.PromptConfig = orchestration.PromptConfig{
    Domain: "gdpr",  // Controls which branch in the template
    Template: `{{if eq .Domain "gdpr"}}
You are operating under GDPR. Data minimization applies. Response: one calendar month (Art. 12).
{{else if eq .Domain "ccpa"}}
You are operating under CCPA. Right to Know, Delete, Opt-Out. Response: 45 days.
{{else}}
You are a privacy-respecting support agent.
{{end}}

Available Capabilities:
{{.CapabilityInfo}}

{{.TypeRules}}

Request: {{.Request}}

{{.ConcreteExample}}

Response (JSON only):`,
}
```

#### Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  config.PromptConfig.TemplateFile = "/config/prompt.tmpl"  (ConfigMap mount) │
│  config.PromptConfig.Domain = "gdpr"                                         │
└──────────────────────────────────┬──────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  CreateOrchestrator() → Factory Selection (factory.go:121-144)               │
│                                                                              │
│  if config.PromptConfig.TemplateFile != "" {                                │
│      builder := NewTemplatePromptBuilder(&config.PromptConfig)              │
│      orchestrator.SetPromptBuilder(builder)  // Uses your template           │
│  }                                                                          │
└──────────────────────────────────┬──────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  TemplatePromptBuilder.BuildPlanningPrompt() (template_prompt_builder.go)    │
│                                                                              │
│  data := TemplateData{                                                      │
│      CapabilityInfo: "customer-data-tool: get_data(...)",  // From discovery │
│      Request:        "I want to download my data",          // User request  │
│      TypeRules:      "customer_id → JSON strings...",       // From config   │
│      Domain:         "gdpr",                                // From config   │
│      ConcreteExample: buildConcreteExample(),                // Few-shot     │
│  }                                                                          │
│                                                                              │
│  template.Execute(&buf, data)  // Renders prompt.tmpl (GDPR template)        │
└──────────────────────────────────┬──────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Rendered Prompt → LLM                                                       │
│                                                                              │
│  You are a customer data access orchestrator under GDPR...                   │
│  • Data Minimization (Art. 5)                                                │
│  • Right of Access (Art. 15)                                                 │
│  • Response: one calendar month (Art. 12)                                    │
│  ...                                                                        │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### Compliance Mapping

| Regulation | Template File | Domain Value | Key Sections Included |
|------------|---------------|--------------|----------------------|
| **GDPR** (EU) | `eu-gdpr.tmpl` | `gdpr` | Art. 5 (minimization), Art. 15-20 (rights), Art. 30 (records) |
| **CCPA** (California) | `us-ccpa.tmpl` | `ccpa` | §1798.100-125 (know, delete, opt-out, non-discrimination) |
| **LGPD** (Brazil) | `br-lgpd.tmpl` | `lgpd` | Art. 18 (holder rights), Art. 37 (records) |
| **PDPA** (Singapore) | `sg-pdpa.tmpl` | `pdpa` | Access, correction, withdrawal obligations |

#### Key Points

1. **Templates are loaded from files** - Use ConfigMaps in Kubernetes for easy updates without redeployment.

2. **Graceful degradation** - If template execution fails, TemplatePromptBuilder falls back to DefaultPromptBuilder ([template_prompt_builder.go:188-190](../../orchestration/template_prompt_builder.go#L188-L190)).

3. **Type rules are shared** - Put common type rules in `PromptConfig.AdditionalTypeRules`. Templates inherit them via `{{.TypeRules}}`.

4. **Legal review workflow** - Templates are plain text files that legal/compliance teams can review and approve without touching code.

5. **Domain drives conditional logic** - Use `{{if eq .Domain "gdpr"}}` in templates for region-specific sections.

6. **Security warning** - Templates must come from trusted sources only. Never accept templates from user input. See [template_prompt_builder.go:17-20](../../orchestration/template_prompt_builder.go#L17-L20).

---

## Advanced: Custom PromptBuilder

For complete control, implement the `PromptBuilder` interface. This is Layer 3—use it when you need to call external services, apply ML-based prompt optimization, or meet strict compliance requirements.

### When to Use Layer 3

| Scenario | Why Layer 3 |
|----------|-------------|
| **SOC 2 / SOX audit logging** | Must log every prompt for compliance audit trail |
| **RAG-enhanced capability selection** | Query vector DB to select relevant tools from 500+ available |
| **Multi-tenant isolation** | Filter capabilities based on tenant's subscription tier |
| **PII redaction** | Scan and redact sensitive data before sending to LLM |
| **A/B testing prompts** | Route to different prompt versions based on experiment ID |

### The Interface

The `PromptBuilder` interface is intentionally minimal—one method:

```go
// From orchestration/prompt_builder.go
type PromptBuilder interface {
    BuildPlanningPrompt(ctx context.Context, input PromptInput) (string, error)
}
```

The orchestrator calls this method before every LLM planning request. You receive all the data you need in `PromptInput`, and you return the complete prompt string.

### Optional: SystemPromptBuilder

If your `PromptBuilder` also implements `SystemPromptBuilder`, the orchestrator uses it to construct the system-level message for LLM providers that support separate system/user message roles (Anthropic, OpenAI, Gemini). This enables persona and behavioral rules to be placed in the high-priority system message rather than the user prompt.

```go
// From orchestration/prompt_builder.go
type SystemPromptBuilder interface {
    BuildSystemPrompt(ctx context.Context, input PromptInput) string
}
```

If not implemented, the orchestrator falls back to `PromptConfig.SystemInstructions` + a default orchestrator role description. Both `DefaultPromptBuilder` and `TemplatePromptBuilder` implement this interface.

### Understanding PromptInput

Before building a custom PromptBuilder, understand what data you receive:

```go
// From orchestration/prompt_builder.go
type PromptInput struct {
    // CapabilityInfo is the formatted string of available agents and tools.
    // This comes from the CapabilityProvider (discovery-based or service-based).
    // Example: "weather-tool: get_weather(city: string, units: string) - Get current weather"
    CapabilityInfo string

    // Request is the user's natural language request.
    // Example: "What's the weather in Tokyo and convert 1000 USD to JPY?"
    Request string

    // Metadata contains optional context passed from ProcessRequest().
    // You control what goes here when calling the orchestrator.
    // Examples: {"user_id": "123", "tenant_id": "acme-corp", "session_id": "abc"}
    Metadata map[string]interface{}
}
```

### Detailed Example: SOC 2 Audit-Logging PromptBuilder

This example shows a PromptBuilder for a financial services company that must:
1. Log every LLM prompt for SOC 2 Type II audit
2. Redact PII before logging
3. Include user context for access control verification
4. Track prompt latency metrics

**Business Context**: A bank's loan processing agent must maintain audit trails showing exactly what prompts were sent to the LLM, which user triggered them, and what data was included—without logging actual PII like SSNs.

```go
package main

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "regexp"
    "strings"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/orchestration"
)

// Context keys for request-scoped values (type-safe, avoids collisions)
// These are used to pass data through context.Context to BuildPlanningPrompt
type contextKey string

const (
    ctxKeyUserID    contextKey = "user_id"
    ctxKeyTenantID  contextKey = "tenant_id"
    ctxKeySessionID contextKey = "session_id"
)

// =============================================================================
// STEP 1: Define the external services your PromptBuilder depends on
// =============================================================================

// AuditLogger writes immutable audit records for SOC 2 compliance.
// In production, this would write to an append-only audit log system
// like AWS CloudTrail, Splunk, or a dedicated audit database.
type AuditLogger interface {
    LogPromptGeneration(ctx context.Context, record AuditRecord) error
}

// AuditRecord contains all data required for SOC 2 audit trail.
// This is what gets written to your audit log system.
type AuditRecord struct {
    Timestamp       time.Time              `json:"timestamp"`
    UserID          string                 `json:"user_id"`
    SessionID       string                 `json:"session_id"`
    TenantID        string                 `json:"tenant_id"`
    RequestHash     string                 `json:"request_hash"`      // SHA-256 of original request (for integrity)
    RequestPreview  string                 `json:"request_preview"`   // First 100 chars, PII-redacted
    CapabilityCount int                    `json:"capability_count"`  // How many tools were available
    PromptLength    int                    `json:"prompt_length"`     // Final prompt size in chars
    BuildDurationMs int64                  `json:"build_duration_ms"` // How long prompt building took
    Metadata        map[string]interface{} `json:"metadata"`          // Additional context
}

// MetricsRecorder tracks operational metrics.
// In production, this would be Prometheus, Datadog, or similar.
type MetricsRecorder interface {
    RecordHistogram(name string, value float64, tags map[string]string)
    IncrementCounter(name string, tags map[string]string)
}

// =============================================================================
// STEP 2: Define the PromptBuilder struct with all dependencies
// =============================================================================

// AuditingPromptBuilder implements orchestration.PromptBuilder with full
// SOC 2 audit logging, PII redaction, and metrics.
type AuditingPromptBuilder struct {
    // auditLogger writes to your SOC 2-compliant audit log system.
    // Injected at construction time.
    auditLogger AuditLogger

    // metrics records operational metrics (latency, counts).
    // Injected at construction time.
    metrics MetricsRecorder

    // frameworkLogger is the truvag3 logger for operational logging.
    // This is different from audit logging—this goes to your app logs.
    frameworkLogger core.Logger

    // defaultTypeRules are the type rules always included in the prompt.
    // These come from your base configuration.
    defaultTypeRules []orchestration.TypeRule

    // piiPatterns are regex patterns for PII detection/redaction.
    // Compiled once at construction time for performance.
    piiPatterns []*regexp.Regexp
}

// NewAuditingPromptBuilder creates a new builder with all dependencies.
// This is called once when setting up your orchestrator.
func NewAuditingPromptBuilder(
    auditLogger AuditLogger,
    metrics MetricsRecorder,
    logger core.Logger,
) *AuditingPromptBuilder {
    return &AuditingPromptBuilder{
        auditLogger:     auditLogger,
        metrics:         metrics,
        frameworkLogger: logger,

        // Default type rules for financial services
        defaultTypeRules: []orchestration.TypeRule{
            {
                TypeNames:   []string{"amount", "balance", "principal"},
                JsonType:    "JSON numbers",
                Example:     "1500.00",
                AntiPattern: `"1500.00"`,
                Description: "Monetary amounts with decimal precision",
            },
            {
                TypeNames:   []string{"account_number", "routing_number"},
                JsonType:    "JSON strings",
                Example:     `"****5678"`,
                Description: "Account identifiers (masked)",
            },
        },

        // Compile PII patterns once for performance.
        // NOTE: Regex-based PII detection is a baseline approach suitable for
        // structured, predictable data. For production systems handling diverse
        // free-text inputs, consider:
        //   - ML-based NER (Named Entity Recognition) for context-aware detection
        //   - Hybrid approaches (regex for structured + ML for unstructured)
        //   - Third-party PII detection services (AWS Macie, Google DLP, etc.)
        piiPatterns: []*regexp.Regexp{
            regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),           // SSN: 123-45-6789
            regexp.MustCompile(`\b\d{9}\b`),                       // SSN without dashes
            regexp.MustCompile(`\b\d{16}\b`),                      // Credit card number
            regexp.MustCompile(`\b\d{4}[- ]?\d{4}[- ]?\d{4}[- ]?\d{4}\b`), // CC with spaces/dashes
            regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`), // Email
            regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`),   // US phone: 123-456-7890
        },
    }
}

// =============================================================================
// STEP 3: Implement the PromptBuilder interface
// =============================================================================

// BuildPlanningPrompt is called by the orchestrator before each LLM request.
//
// Parameters:
//   - ctx: Contains request-scoped values, deadlines, and cancellation.
//          May contain trace IDs, user context from middleware, etc.
//   - input: All data needed to build the prompt (see PromptInput struct above)
//
// Returns:
//   - The complete prompt string ready for the LLM
//   - Error if prompt building fails (will abort the orchestration)
func (b *AuditingPromptBuilder) BuildPlanningPrompt(
    ctx context.Context,
    input orchestration.PromptInput,
) (string, error) {
    startTime := time.Now()

    // -------------------------------------------------------------------------
    // STEP 3a: Extract user context from Go context (not input.Metadata)
    // -------------------------------------------------------------------------
    // NOTE: The orchestrator currently sets input.Metadata to nil (orchestrator.go:1039).
    // Use Go's context.Context to pass request-scoped values instead.
    // This is actually the idiomatic Go pattern for request-scoped data.
    //
    // Your application sets these values before calling ProcessRequest():
    //   ctx = context.WithValue(ctx, ctxKeyUserID, userID)
    //   orchestrator.ProcessRequest(ctx, request, nil)

    userID := extractFromContext(ctx, ctxKeyUserID, "anonymous")
    tenantID := extractFromContext(ctx, ctxKeyTenantID, "default")
    sessionID := extractFromContext(ctx, ctxKeySessionID, "")

    // -------------------------------------------------------------------------
    // STEP 3b: Redact PII from the request for audit logging
    // -------------------------------------------------------------------------
    // We need to log what was requested, but we can't log actual PII.
    // Redact SSNs, credit cards, emails before logging.

    redactedRequest := b.redactPII(input.Request)
    requestPreview := truncateString(redactedRequest, 100)

    // -------------------------------------------------------------------------
    // STEP 3c: Build the type rules section
    // -------------------------------------------------------------------------
    // Type rules teach the LLM about your domain's data types.
    // We format them into a human-readable section for the prompt.

    typeRulesSection := b.formatTypeRules(b.defaultTypeRules)

    // -------------------------------------------------------------------------
    // STEP 3d: Build the complete prompt
    // -------------------------------------------------------------------------
    // This is what gets sent to the LLM. Structure it clearly:
    //   1. Role and context
    //   2. Available capabilities (from CapabilityProvider)
    //   3. Type formatting rules
    //   4. The user's request
    //   5. Output format instructions

    prompt := fmt.Sprintf(`You are a financial services orchestrator for tenant %s.

COMPLIANCE REQUIREMENTS:
- All monetary amounts must preserve decimal precision
- Account numbers must be masked (show only last 4 digits)
- Log all operations with user_id for audit trail
- Never include SSN, full account numbers, or passwords in responses

AVAILABLE CAPABILITIES:
%s

TYPE FORMATTING RULES:
%s

USER REQUEST:
%s

Generate a JSON execution plan. The plan must:
1. Use only the capabilities listed above
2. Follow the type formatting rules exactly
3. Include audit metadata in each step

Response format:
{
  "steps": [
    {
      "id": "step-1",
      "tool": "tool-name",
      "action": "action_name",
      "params": { ... },
      "audit": { "user_id": "%s", "reason": "description" }
    }
  ]
}

Response (JSON only):`,
        tenantID,
        input.CapabilityInfo,  // Comes from CapabilityProvider
        typeRulesSection,
        input.Request,         // The user's original request
        userID,
    )

    // -------------------------------------------------------------------------
    // STEP 3e: Record audit log (SOC 2 requirement)
    // -------------------------------------------------------------------------
    // This creates an immutable record of what prompt was generated.
    // Auditors can verify: who requested what, when, and what data was included.

    buildDuration := time.Since(startTime)

    auditRecord := AuditRecord{
        Timestamp:       time.Now().UTC(),
        UserID:          userID,
        SessionID:       sessionID,
        TenantID:        tenantID,
        RequestHash:     hashString(input.Request), // Hash for integrity verification
        RequestPreview:  requestPreview,            // PII-redacted preview
        CapabilityCount: countCapabilities(input.CapabilityInfo),
        PromptLength:    len(prompt),
        BuildDurationMs: buildDuration.Milliseconds(),
        Metadata: map[string]interface{}{
            "type_rules_count": len(b.defaultTypeRules),
        },
    }

    if err := b.auditLogger.LogPromptGeneration(ctx, auditRecord); err != nil {
        // Audit logging failure is a compliance violation—fail the request
        b.frameworkLogger.Error("Audit logging failed", map[string]interface{}{
            "error":      err.Error(),
            "user_id":    userID,
            "session_id": sessionID,
        })
        return "", fmt.Errorf("audit logging failed: %w", err)
    }

    // -------------------------------------------------------------------------
    // STEP 3f: Record operational metrics
    // -------------------------------------------------------------------------
    // These metrics help you monitor performance and detect anomalies.

    b.metrics.RecordHistogram(
        "prompt_builder.build_duration_ms",
        float64(buildDuration.Milliseconds()),
        map[string]string{"tenant_id": tenantID},
    )
    b.metrics.IncrementCounter(
        "prompt_builder.prompts_generated",
        map[string]string{"tenant_id": tenantID},
    )

    // -------------------------------------------------------------------------
    // STEP 3g: Return the complete prompt
    // -------------------------------------------------------------------------
    // The orchestrator will send this to the LLM via the AIClient.

    return prompt, nil
}

// =============================================================================
// STEP 4: Helper methods
// =============================================================================

// redactPII replaces sensitive patterns with [REDACTED]
func (b *AuditingPromptBuilder) redactPII(text string) string {
    result := text
    for _, pattern := range b.piiPatterns {
        result = pattern.ReplaceAllString(result, "[REDACTED]")
    }
    return result
}

// formatTypeRules converts TypeRule slice to prompt-friendly text
func (b *AuditingPromptBuilder) formatTypeRules(rules []orchestration.TypeRule) string {
    var lines []string
    for _, rule := range rules {
        line := fmt.Sprintf("- For %s: use %s (example: %s)",
            strings.Join(rule.TypeNames, ", "),
            rule.JsonType,
            rule.Example,
        )
        if rule.AntiPattern != "" {
            line += fmt.Sprintf(" NOT %s", rule.AntiPattern)
        }
        lines = append(lines, line)
    }
    return strings.Join(lines, "\n")
}

// Helper functions

// extractFromContext extracts a string value from context using typed key
func extractFromContext(ctx context.Context, key contextKey, defaultVal string) string {
    if v, ok := ctx.Value(key).(string); ok {
        return v
    }
    return defaultVal
}

func truncateString(s string, maxLen int) string {
    if len(s) <= maxLen {
        return s
    }
    return s[:maxLen] + "..."
}

func hashString(s string) string {
    h := sha256.Sum256([]byte(s))
    return hex.EncodeToString(h[:])
}

func countCapabilities(capabilityInfo string) int {
    // Simple heuristic: count lines that look like capability definitions
    lines := strings.Split(capabilityInfo, "\n")
    count := 0
    for _, line := range lines {
        if strings.Contains(line, ":") && strings.Contains(line, "(") {
            count++
        }
    }
    return count
}

// =============================================================================
// STEP 5: Wire it up to the orchestrator
// =============================================================================

func SetupOrchestratorWithAuditBuilder(
    discovery core.Discovery,
    aiClient core.AIClient,
    auditLogger AuditLogger,  // Your SOC 2 audit log system
    metrics MetricsRecorder,  // Your metrics system (Prometheus, Datadog)
    logger core.Logger,       // Your app logger
) (*orchestration.AIOrchestrator, error) {

    // Create the custom PromptBuilder
    promptBuilder := NewAuditingPromptBuilder(auditLogger, metrics, logger)

    // Create orchestrator config
    config := orchestration.DefaultConfig()
    config.RoutingMode = orchestration.ModeAutonomous

    // Inject the custom PromptBuilder via dependencies
    // This is the key step—PromptBuilder goes in OrchestratorDependencies
    deps := orchestration.OrchestratorDependencies{
        Discovery:     discovery,  // From your Redis/service discovery
        AIClient:      aiClient,   // From ai.NewClient(...)
        Logger:        logger,     // Your app logger
        PromptBuilder: promptBuilder, // <-- YOUR CUSTOM BUILDER
    }

    // CreateOrchestrator sees deps.PromptBuilder != nil
    // and uses it instead of DefaultPromptBuilder
    return orchestration.CreateOrchestrator(config, deps)
}

// =============================================================================
// STEP 6: Passing context to your PromptBuilder via Go context
// =============================================================================

// IMPORTANT: Currently, the orchestrator's buildPlanningPrompt() sets Metadata to nil
// (see orchestrator.go:1039). The metadata passed to ProcessRequest() is stored in
// the response but NOT forwarded to PromptBuilder.
//
// WORKAROUND: Use Go's context.Context to pass request-scoped data to your PromptBuilder.
// This is actually the preferred pattern for request-scoped values in Go.
// The context keys (ctxKeyUserID, etc.) are defined at the top of this file.

// WithRequestContext adds request-scoped values to context
func WithRequestContext(ctx context.Context, userID, tenantID, sessionID string) context.Context {
    ctx = context.WithValue(ctx, ctxKeyUserID, userID)
    ctx = context.WithValue(ctx, ctxKeyTenantID, tenantID)
    ctx = context.WithValue(ctx, ctxKeySessionID, sessionID)
    return ctx
}

func ProcessLoanApplication(
    ctx context.Context,
    orchestrator *orchestration.AIOrchestrator,
    request string,
    userID string,      // From your auth middleware (JWT token)
    tenantID string,    // From your multi-tenant context
    sessionID string,   // From request headers
) (*orchestration.OrchestratorResponse, error) {

    // Add context values that your PromptBuilder will extract
    ctx = WithRequestContext(ctx, userID, tenantID, sessionID)

    // ProcessRequest passes ctx to BuildPlanningPrompt(ctx, input)
    // Your PromptBuilder extracts values from ctx, not input.Metadata
    return orchestrator.ProcessRequest(ctx, request, nil)
}
```

### Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  Your Application                                                            │
│                                                                              │
│  userID := getFromJWT(ctx)           ←── From auth middleware               │
│  tenantID := getTenantFromHost(r)    ←── From request routing               │
│  sessionID := r.Header.Get("X-Session-ID")                                  │
│                                                                              │
│  // Add values to Go context (idiomatic Go pattern)                          │
│  ctx = context.WithValue(ctx, ctxKeyUserID, userID)                          │
│  ctx = context.WithValue(ctx, ctxKeyTenantID, tenantID)                      │
│  ctx = context.WithValue(ctx, ctxKeySessionID, sessionID)                    │
│                                                                              │
│  orchestrator.ProcessRequest(ctx, request, nil)                              │
└──────────────────────────────────┬──────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  AIOrchestrator.ProcessRequest()                                             │
│                                                                              │
│  1. capabilityInfo := capabilityProvider.GetCapabilities(ctx, request)       │
│     └── Returns formatted string of available tools from discovery           │
│                                                                              │
│  2. promptInput := PromptInput{                                              │
│         CapabilityInfo: capabilityInfo,    ←── From CapabilityProvider       │
│         Request:        request,            ←── From your app                │
│         Metadata:       nil,                ←── Currently hardcoded to nil   │
│     }                                                                        │
│                                                                              │
│  3. prompt := promptBuilder.BuildPlanningPrompt(ctx, promptInput)            │
│     └── ctx carries your request-scoped values!                              │
└──────────────────────────────────┬──────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  YOUR AuditingPromptBuilder.BuildPlanningPrompt(ctx, input)                  │
│                                                                              │
│  // Extract context from ctx (NOT input.Metadata which is nil)               │
│  userID := ctx.Value(ctxKeyUserID).(string)                                  │
│  tenantID := ctx.Value(ctxKeyTenantID).(string)                              │
│                                                                              │
│  // Use CapabilityInfo (formatted list of tools)                             │
│  // This came from CapabilityProvider, you just include it in your prompt    │
│  prompt := fmt.Sprintf("...%s...", input.CapabilityInfo)                     │
│                                                                              │
│  // Log to audit system (SOC 2 requirement)                                  │
│  auditLogger.LogPromptGeneration(ctx, auditRecord)                           │
│                                                                              │
│  return prompt, nil                                                          │
└──────────────────────────────────┬──────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│  Back to AIOrchestrator                                                      │
│                                                                              │
│  4. aiClient.Generate(ctx, prompt)  ←── Send YOUR prompt to LLM              │
│  5. Parse JSON response as execution plan                                    │
│  6. Execute each step by calling tools                                       │
│  7. Return results                                                           │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Key Points

1. **`PromptInput.CapabilityInfo`** - You don't create this. The orchestrator gets it from the CapabilityProvider (which queries discovery). You just include it in your prompt.

2. **`PromptInput.Request`** - This is exactly what your app passed to `ProcessRequest()`. You include it in your prompt (possibly after redaction).

3. **`PromptInput.Metadata`** - Currently hardcoded to `nil` in the orchestrator ([orchestrator.go:1039](../../orchestration/orchestrator.go#L1039)). **Use Go's `context.Context` instead** to pass request-scoped values like user ID, tenant ID, etc. This is actually the idiomatic Go pattern.

4. **`context.Context` carries your data** - Whatever you add to the context before calling `ProcessRequest()` is available in your `BuildPlanningPrompt(ctx, input)` method. Extract with `ctx.Value(key)`.

5. **Audit logging failures should fail the request** - If you can't log, you shouldn't process. This is a compliance requirement.

6. **Inject via `OrchestratorDependencies.PromptBuilder`** - The factory checks this field first ([factory.go:114](../../orchestration/factory.go#L114)). If set, it uses your builder instead of DefaultPromptBuilder.

### SOC 2 Type II Compliance Notes

The example above satisfies these SOC 2 Trust Service Criteria:

| Criterion | How the Example Addresses It |
|-----------|------------------------------|
| **CC6.1** (Logical Access) | `userID` and `tenantID` logged for every prompt, enabling access tracking |
| **CC7.2** (System Changes) | `requestHash` provides integrity verification; `PromptLength` tracks changes |
| **CC8.1** (Incident Response) | `sessionID` enables correlation across request lifecycle for investigation |
| **PI1.1** (PII Protection) | `redactPII()` removes SSN, CC, email, phone before logging |

**Audit Evidence Requirements**: SOC 2 Type II audits verify controls operate effectively over time (typically 6-12 months). The `AuditRecord` structure produces evidence that auditors can sample:

- **Completeness**: Every prompt generates a record (no sampling gaps)
- **Accuracy**: `requestHash` allows verification that records weren't modified
- **Timeliness**: `Timestamp` in UTC enables chronological analysis
- **Authorization**: `userID` + `tenantID` proves access was authorized

**Why Audit Failures Must Fail Requests**: Per CC7.1 (Change Management), if audit logging fails, the control is not operating effectively. Processing the request without logging creates a gap in the audit trail, which auditors will flag as a control deficiency.

---

## Environment Variable Configuration

For Kubernetes and containerized deployments, you can configure prompts via environment variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `TRUVAG3_PROMPT_TEMPLATE_FILE` | Path to template file | `/config/prompt.tmpl` |
| `TRUVAG3_PROMPT_DOMAIN` | Domain context | `healthcare` |
| `TRUVAG3_PROMPT_TYPE_RULES` | JSON array of type rules | See below |
| `TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS` | JSON array of instructions | See below |

### Example: Environment-Based Configuration

```bash
# Set domain
export TRUVAG3_PROMPT_DOMAIN="finance"

# Set type rules as JSON
export TRUVAG3_PROMPT_TYPE_RULES='[
  {"type_names":["amount","price"],"json_type":"JSON numbers","example":"1500.00"},
  {"type_names":["account_id"],"json_type":"JSON strings","example":"\"00123456\""}
]'

# Set custom instructions
export TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS='[
  "Maintain decimal precision for all monetary calculations",
  "Log all financial operations for SOX compliance"
]'
```

### Loading in Code

```go
config := &orchestration.PromptConfig{}
if err := config.LoadFromEnv(); err != nil {
    log.Printf("Warning: Could not load prompt config from env: %v", err)
}

// Or panic on error (for required configuration)
config.MustLoadFromEnv()
```

---

## Kubernetes Deployment

### ConfigMap for Templates

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: orchestrator-prompt
  namespace: truvag3
data:
  planning-prompt.tmpl: |
    You are an AI orchestrator for the {{.Domain}} domain.

    Available Capabilities:
    {{.CapabilityInfo}}

    User Request: {{.Request}}

    Type Rules:
    {{.TypeRules}}

    {{if .CustomInstructions}}
    Additional Instructions:
    {{.CustomInstructions}}
    {{end}}

    Generate a JSON execution plan:
    {{.ConcreteExample}}

    Response (JSON only):
```

### Deployment with ConfigMap

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-agent
spec:
  template:
    spec:
      containers:
      - name: agent
        image: my-agent:latest
        env:
        - name: TRUVAG3_PROMPT_TEMPLATE_FILE
          value: /config/planning-prompt.tmpl
        - name: TRUVAG3_PROMPT_DOMAIN
          value: healthcare
        volumeMounts:
        - name: prompt-config
          mountPath: /config
          readOnly: true
      volumes:
      - name: prompt-config
        configMap:
          name: orchestrator-prompt
```

---

## Troubleshooting

### Type Rules Not Applied

**Symptoms:** LLM still generates wrong types despite your rules.

**Check:**
1. Verify `TypeNames` matches the actual parameter names in your tools
2. Ensure rules are in `AdditionalTypeRules` array
3. Check logs for `type_rules_count` attribute

```go
// Debug: Print what the prompt looks like
builder, _ := orchestration.NewDefaultPromptBuilder(&config.PromptConfig)
prompt, _ := builder.BuildPlanningPrompt(ctx, orchestration.PromptInput{
    CapabilityInfo: "your-tools-here",
    Request:        "test request",
})
fmt.Println(prompt)  // See if your rules appear
```

### Template Not Loading

**Symptoms:** Warning about falling back to default builder.

**Check:**
1. File path is correct and readable
2. Template syntax is valid Go `text/template`
3. In Kubernetes, verify ConfigMap is mounted

```bash
# Check if file exists in container
kubectl exec -it my-pod -- cat /config/planning-prompt.tmpl
```

### Graceful Degradation

The system is designed to never crash due to configuration issues:

| Problem | Behavior |
|---------|----------|
| Invalid template syntax | Falls back to DefaultPromptBuilder |
| Missing template file | Falls back to DefaultPromptBuilder |
| Template execution error | Falls back to DefaultPromptBuilder |

You'll see warnings in logs, but the system keeps running.

---

## Quick Reference

### PromptConfig Structure

```go
type PromptConfig struct {
    // Persona customization (similar to LangChain's system_prompt)
    SystemInstructions string // Orchestrator's core behavioral context

    Domain              string     // Domain context (healthcare, finance, etc.)
    AdditionalTypeRules []TypeRule // Type formatting rules
    CustomInstructions  []string   // Domain-specific guidance

    // Layer 2: Templates
    TemplateFile string // Path to template file (takes precedence)
    Template     string // Inline template string

    // Options
    IncludeAntiPatterns *bool // Include "what NOT to do" examples (default: true)
}
```

**SystemInstructions** is the recommended way to define your orchestrator's persona. When set, your persona becomes the primary identity, and the orchestrator role becomes a functional description. Example:

```go
config.PromptConfig = orchestration.PromptConfig{
    SystemInstructions: `You are a travel planning assistant.
Always check weather before recommending outdoor activities.
Prefer real-time data sources over cached information.`,
    Domain: "travel",
}
```

### TypeRule Structure

```go
type TypeRule struct {
    TypeNames   []string // Field names this applies to
    JsonType    string   // "JSON strings", "JSON numbers", "JSON integers", etc.
    Example     string   // Correct format example
    AntiPattern string   // What NOT to do (optional)
    Description string   // Explanation (optional)
}
```

### Built-in Domain Behaviors

Only these three domains have automatic built-in additions:

| Domain | Built-in Additions |
|--------|-------------------|
| `healthcare` | PHI protection, HIPAA compliance, audit requirements |
| `finance` | Decimal precision, transaction tracking, SOX compliance |
| `legal` | Chain of custody, timestamp attribution |

**Note**: Other domain values (like `travel`, `retail`, `scientific`) work perfectly fine—they just don't get automatic additions. Your `AdditionalTypeRules` and `CustomInstructions` still apply. The `Domain` field for non-built-in domains serves as documentation and can be used in custom templates via `{{.Domain}}`.

### Source Files

| File | Purpose |
|------|---------|
| [orchestration/prompt_builder.go](../../orchestration/prompt_builder.go) | Interface and config structs |
| [orchestration/default_prompt_builder.go](../../orchestration/default_prompt_builder.go) | Layer 1 implementation |
| [orchestration/template_prompt_builder.go](../../orchestration/template_prompt_builder.go) | Layer 2 implementation |
| [orchestration/prompt_config_env.go](../../orchestration/prompt_config_env.go) | Environment loading |
| [orchestration/factory.go](../../orchestration/factory.go) | Builder selection logic |

---

## What's Next?

Now that you understand domain configuration, you might want to explore:

- [Orchestration README](../../orchestration/README.md) - Full orchestration module documentation
- [Chat Agent Guide](../CHAT_AGENT_GUIDE.md) - Building streaming chat agents
- [Async Orchestration Guide](../ASYNC_ORCHESTRATION_GUIDE.md) - Long-running tasks
- [Distributed Tracing Guide](../DISTRIBUTED_TRACING_GUIDE.md) - Observability

Happy building! If something doesn't work as expected, check the troubleshooting section or look at the example agents in the `examples/` directory.
