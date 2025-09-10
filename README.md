# ✅ UPDATED: README.md - Method names and collection names

# Found Footy - Enterprise Football Data Pipeline

## 🎯 **Executive Summary**

Found Footy is an **enterprise-grade, real-time football data processing platform** built with Prefect 3 and modern microservices architecture. The system features **domain-separated flows** with dedicated worker pools for maximum clarity and scalability.

### **Key Business Value:**
- ⚡ **Sub-3-minute goal detection** - Average 90-second response to scoring events
- 🏗️ **Domain-separated architecture** - Clean separation with dedicated worker pools
- 🔄 **Zero-downtime monitoring** - Continuous 24/7 operation with intelligent resource management
- 🎯 **Direct flow triggering** - No automation complexity, pure `run_deployment()` calls
- 📊 **Status-driven lifecycle** - Intelligent fixture routing based on FIFA API status codes
- 🚀 **Rich flow naming** - Contextual names for instant debugging clarity

## 🏗️ **Architecture Overview**

### **🌊 Domain-Separated Flow Architecture**

```mermaid
graph TB
    %% External Triggers
    Daily[⏰ Daily Schedule<br/>00:05 UTC] --> IF[ingest-flow<br/>ingest-pool]
    Monitor[⏰ Monitor Schedule<br/>Every 3 minutes] --> MF[monitor-flow<br/>monitor-pool]
    Manual[🖱️ Manual Trigger] --> IF
    
    %% Ingest Flow Domain - Status-Based Routing
    IF --> ST1[shared_tasks:<br/>fixtures_process_parameters_task]
    ST1 --> ST2[shared_tasks:<br/>fixtures_fetch_api_task]
    ST2 --> ST3[shared_tasks:<br/>fixtures_categorize_task<br/>STATUS-DRIVEN ROUTING]
    ST3 --> |NS TBD + future time| STAGING[Store to fixtures_staging]
    ST3 --> |1H 2H HT LIVE| ACTIVE[Store to fixtures_active]
    ST3 --> |FT AET PEN etc| COMPLETED[Store to fixtures_completed]
    
    %% Advance Flow Domain
    STAGING --> SCHED[📅 Scheduled Advancement<br/>3min before kickoff]
    SCHED --> AF[advance-flow<br/>advance-pool]
    AF --> ST4[shared_tasks:<br/>fixtures_advance_task]
    ST4 --> |Move staging to active| FA[(fixtures_active)]
    
    %% Monitor Flow Domain - Dedicated Pool
    MF --> CHECK{Active fixtures?}
    CHECK -->|No| SKIP[⏸️ Skip API calls<br/>Continue running]
    CHECK -->|Yes| MT[monitor_flow:<br/>fixtures_monitor_task]
    MT --> ST5[shared_tasks:<br/>fixtures_delta_task<br/>BULK COLLECTION SCAN]
    ST5 --> |Goals changed| GOAL_TRIGGER[🎯 Direct run_deployment]
    ST5 --> |Status completion| COMP_TRIGGER[🏁 Direct run_deployment]
    
    %% Goal Flow Domain - Direct Triggering
    GOAL_TRIGGER --> GF[goal-flow<br/>goal-pool]
    GF --> STORE_GOAL[Store goals with validation]
    STORE_GOAL --> TWITTER_TRIGGER[🐦 Direct run_deployment<br/>with rich naming]
    
    %% Twitter Flow Domain
    TWITTER_TRIGGER --> TF[twitter-flow<br/>twitter-pool]
    TF --> PROCESS[Process and post goal]
    PROCESS --> |Move processed goal| GP[(goals_processed)]
    
    %% Completion Flow
    COMP_TRIGGER --> AF2[advance-flow<br/>advance-pool]
    AF2 --> |active to completed| FC[(fixtures_completed)]
    
    %% Data Collections
    FS[(fixtures_staging)] --> |Time-based advance| FA
    FA --> |Status-based complete| FC
    GA[(goals_active<br/>Validation + Deduplication)] --> |Direct triggering| GP
    TS[(teams<br/>Enhanced metadata)] --> ST3
    
    %% Worker Pool Isolation with Clear Logs
    IF -.-> POOL1[ingest-pool<br/>Pure ingestion logs]
    MF -.-> POOL2[monitor-pool<br/>Goal detection only]
    AF -.-> POOL3[advance-pool<br/>Collection movement]
    GF -.-> POOL4[goal-pool<br/>Goal processing]
    TF -.-> POOL5[twitter-pool<br/>Social media only]
    
    classDef ingest fill:#e1f5fe,stroke:#01579b,stroke-width:2px,color:#000
    classDef monitor fill:#e8f5e8,stroke:#2e7d32,stroke-width:3px,color:#000
    classDef goal fill:#fff3e0,stroke:#e65100,stroke-width:2px,color:#000
    classDef shared fill:#f3e5f5,stroke:#4a148c,stroke-width:2px,color:#000
    classDef collection fill:#e8f5e8,stroke:#1b5e20,stroke-width:2px,color:#000
    classDef pool fill:#fce4ec,stroke:#880e4f,stroke-width:2px,color:#000
    
    class IF,AF,TF ingest
    class MF,MT monitor
    class GF,TWITTER_TRIGGER,GOAL_TRIGGER goal
    class ST1,ST2,ST3,ST4,ST5 shared
    class FS,FA,FC,GA,GP,TS collection
    class POOL1,POOL2,POOL3,POOL4,POOL5 pool
```

### **📊 Enhanced Data Pipeline**

```mermaid
graph LR
    subgraph "📊 Fixture Lifecycle with Status Routing"
        FS[fixtures_staging<br/>📅 Future matches<br/>Time-based advancement] 
        FA[fixtures_active<br/>🔄 Live monitoring<br/>Goal detection enabled]
        FC[fixtures_completed<br/>🏁 Archived<br/>Historical data]
        FS --> |advance-flow| FA
        FA --> |advance-flow| FC
    end
    
    subgraph "⚽ Goal Pipeline with Direct Triggering" 
        GP_PENDING[goals_pending<br/>🎯 Validated goals only<br/>Duplicate prevention<br/>Complete data guarantee]
        GP_PROCESSED[goals_processed<br/>✅ Twitter posted<br/>Archived goals]
        GP_PENDING --> |twitter-flow| GP_PROCESSED
    end
    
    subgraph "🔧 Shared Tasks Domain"
        ST[shared_tasks.py<br/>Reusable API calls<br/>Storage operations<br/>Delta detection]
    end
    
    ST --> FA
    FA --> GP_PENDING
    
    classDef staging fill:#fff3e0,stroke:#e65100,stroke-width:2px,color:#000
    classDef active fill:#e8f5e8,stroke:#2e7d32,stroke-width:2px,color:#000
    classDef completed fill:#f3e5f5,stroke:#4a148c,stroke-width:2px,color:#000
    classDef shared fill:#e1f5fe,stroke:#01579b,stroke-width:2px,color:#000
    
    class FS staging
    class FA,GP_PENDING active
    class FC,GP_PROCESSED completed
    class ST shared
```

## 🔧 **Domain-Separated Flow Architecture**

### **📁 Clean File Structure**
```
found_footy/flows/
├── shared_tasks.py          # ✅ Reusable API/storage components
├── ingest_flow.py          # ✅ ingest-flow (Pure ingestion domain)
├── monitor_flow.py         # ✅ monitor-flow (Live monitoring domain)  
├── advance_flow.py         # ✅ advance-flow (Collection movement domain)
├── goal_flow.py            # ✅ goal-flow (Goal processing domain)
├── twitter_flow.py         # ✅ twitter-flow (Social media domain)
├── flow_naming.py          # ✅ Rich naming service
└── flow_triggers.py        # ✅ Async scheduling utilities
```

### **🎯 Flow Responsibilities**

| Flow Name | Domain | Worker Pool | Purpose | Triggers |
|-----------|--------|-------------|---------|----------|
| **ingest-flow** | Ingestion | `ingest-pool` | Status-driven fixture routing | Daily schedule + Manual |
| **monitor-flow** | Monitoring | `monitor-pool` | Live goal detection | Every 3 minutes |
| **advance-flow** | Movement | `advance-pool` | Collection advancement | Scheduled + Event-driven |
| **goal-flow** | Processing | `goal-pool` | Goal validation + Twitter triggering | Monitor-triggered |
| **twitter-flow** | Social Media | `twitter-pool` | Goal posting + archiving | Goal-triggered |

### **🔄 Direct Flow Triggering (No Automation)**

**Key Innovation:** We replaced complex Prefect automations with **direct `run_deployment()` calls** for:

- ✅ **Predictable execution** - No template parsing issues
- ✅ **Rich flow naming** - Uses our `flow_naming.py` service directly
- ✅ **Clear debugging** - Direct cause-and-effect in logs
- ✅ **Non-blocking** - Async execution without hanging

```python
# ✅ EXAMPLE: Direct triggering with rich naming
from found_footy.flows.flow_naming import get_twitter_flow_name  # ✅ UPDATED

twitter_flow_name = get_twitter_flow_name(goal_id)  # ✅ UPDATED

run_deployment(
    name="twitter-flow/twitter-flow",
    parameters={"goal_id": goal_id},
    flow_run_name=twitter_flow_name  # ✅ Rich naming
)

# Result: "⚽ GOAL: Messi (67') for Argentina vs Brazil [#12345]"
```

### **🎯 Flow Naming Service**

Our centralized flow naming service provides rich, contextual names for all flows:

```python
# ✅ FLOW NAMING METHODS - Match flow names exactly
from found_footy.flows.flow_naming import (
    get_ingest_flow_name,     # ✅ ingest-flow
    get_monitor_flow_name,    # ✅ monitor-flow  
    get_advance_flow_name,    # ✅ advance-flow
    get_goal_flow_name,       # ✅ goal-flow
    get_twitter_flow_name     # ✅ twitter-flow
)

# Examples of rich naming
ingest_name = get_ingest_flow_name("20250910", 50)
# Result: "📥 INGEST: Tue Sep 10 (50 teams)"

monitor_name = get_monitor_flow_name()
# Result: "👁️ MONITOR: 14:23:45 - Active Check"

advance_name = get_advance_flow_name("fixtures_staging", "fixtures_active", 12345)
# Result: "🚀 KICKOFF: Barcelona vs Real Madrid [#12345]"

goal_name = get_goal_flow_name(12345, 2)
# Result: "⚽ GOALS: Liverpool 1-0 Arsenal - 2 events [#12345]"

twitter_name = get_twitter_flow_name("12345_67_789")
# Result: "⚽ Liverpool: Salah (67') for Liverpool vs Arsenal [#12345]"
```

### **📊 Data Collections Architecture**

Our 6-collection MongoDB architecture with clear goal pipeline:

| Collection | Purpose | Data Flow |
|------------|---------|-----------|
| `teams` | Team metadata with rankings | Static reference data |
| `fixtures_staging` | Future matches awaiting kickoff | → `fixtures_active` |
| `fixtures_active` | Live matches under monitoring | → `fixtures_completed` |
| `fixtures_completed` | Archived completed matches | Final storage |
| `goals_pending` | ✅ New goals awaiting Twitter posting | → `goals_processed` |
| `goals_processed` | ✅ Goals posted to social media | Final storage |
````