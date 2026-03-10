package main

import (
	"strconv"
	"time"
)

// MaintenanceOrderEvent represents the input from Digital Twin
type MaintenanceOrderEvent struct {
	EquipmentID          string                 `json:"equipmentId" validate:"required"`
	FunctionalLocation   string                 `json:"functionalLocation,omitempty"`
	Plant                string                 `json:"plant" validate:"required"`
	Description          string                 `json:"description" validate:"required"`
	Priority             string                 `json:"priority,omitempty"`
	MaintenanceOrderType string                 `json:"maintenanceOrderType,omitempty"`
	PlannedStartTime     *time.Time             `json:"plannedStartTime,omitempty"`
	PlannedEndTime       *time.Time             `json:"plannedEndTime,omitempty"`
	Operations           []MaintenanceOperation `json:"operations,omitempty"`
}

// MaintenanceOperation represents a single operation within a maintenance order
type MaintenanceOperation struct {
	Text         string  `json:"text" validate:"required"`
	WorkCenter   string  `json:"workCenter,omitempty"`
	Duration     float64 `json:"duration,omitempty"`
	DurationUnit string  `json:"durationUnit,omitempty"`
}

// MaintenanceOrderResponse represents the response after creating an order
type MaintenanceOrderResponse struct {
	MaintenanceOrder        string    `json:"maintenanceOrder"`
	MaintenanceNotification string    `json:"maintenanceNotification"`
	Status                  string    `json:"status"`
	Message                 string    `json:"message"`
	CreatedAt               time.Time `json:"createdAt"`
}

// MaintenanceOrderStatus represents the current status of a maintenance order
type MaintenanceOrderStatus struct {
	MaintenanceOrder        string                 `json:"maintenanceOrder"`
	Status                  string                 `json:"status"`
	Description             string                 `json:"description"`
	Equipment               string                 `json:"equipment"`
	Plant                   string                 `json:"plant"`
	MaintenanceNotification string                 `json:"maintenanceNotification"`
	ActualStartTime         *time.Time             `json:"actualStartTime,omitempty"`
	ActualEndTime           *time.Time             `json:"actualEndTime,omitempty"`
	Operations              []OperationStatus      `json:"operations,omitempty"`
	ObjectList              []ObjectListItemStatus `json:"objectList,omitempty"`
}

// OperationStatus represents the status of a specific operation
type OperationStatus struct {
	MaintenanceOrderOperation string            `json:"maintenanceOrderOperation"`
	Text                      string            `json:"text"`
	Status                    string            `json:"status"`
	ActualWorkQuantity        float64           `json:"actualWorkQuantity,omitempty"`
	WorkQuantityUnit          string            `json:"workQuantityUnit,omitempty"`
	Components                []ComponentStatus `json:"components,omitempty"`
}

// ComponentStatus represents a component used in maintenance
type ComponentStatus struct {
	Material            string  `json:"material"`
	Description         string  `json:"description"`
	RequirementQuantity float64 `json:"requirementQuantity"`
	MaterialUnit        string  `json:"materialUnit"`
	GoodsMovementType   string  `json:"goodsMovementType"`
	Plant               string  `json:"plant,omitempty"`
	StorageLocation     string  `json:"storageLocation,omitempty"`
}

// ObjectListItemStatus represents equipment in the object list
type ObjectListItemStatus struct {
	Equipment          string `json:"equipment"`
	Material           string `json:"material"`
	SerialNumber       string `json:"serialNumber"`
	FunctionalLocation string `json:"functionalLocation,omitempty"`
}

// MaintenanceDoneEvent represents completion notification from SAP
type MaintenanceDoneEvent struct {
	OrderID         string     `json:"orderId" validate:"required"`
	Status          string     `json:"status" validate:"required"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	ActualWorkHours float64    `json:"actualWorkHours,omitempty"`
	Notes           string     `json:"notes,omitempty"`
}

// SAP Notification Request
type SAPNotificationRequest struct {
	NotificationType   string `json:"NotificationType"`
	Description        string `json:"Description"`
	Equipment          string `json:"Equipment"`
	FunctionalLocation string `json:"FunctionalLocation,omitempty"`
	Plant              string `json:"Plant"`
	Priority           string `json:"Priority,omitempty"`
}

// SAP Notification Response
type SAPNotificationResponse struct {
	D struct {
		Notification string `json:"Notification"`
		Description  string `json:"Description"`
		Plant        string `json:"Plant"`
	} `json:"d"`
}

// SAP Order Request
type SAPOrderRequest struct {
	MaintenanceOrderType        string              `json:"MaintenanceOrderType"`
	Description                 string              `json:"Description"`
	Equipment                   string              `json:"Equipment"`
	FunctionalLocation          string              `json:"FunctionalLocation,omitempty"`
	Plant                       string              `json:"Plant"`
	MaintenancePlanningPlant    string              `json:"MaintenancePlanningPlant,omitempty"`
	Priority                    string              `json:"Priority,omitempty"`
	MaintOrdBasicStartDateTime  string              `json:"MaintOrdBasicStartDateTime,omitempty"`
	MaintOrdBasicEndDateTime    string              `json:"MaintOrdBasicEndDateTime,omitempty"`
	MaintenanceNotification     string              `json:"MaintenanceNotification,omitempty"`
	ToMaintenanceOrderOperation []SAPOrderOperation `json:"to_MaintenanceOrderOperation,omitempty"`
}

// SAP Order Operation
type SAPOrderOperation struct {
	OperationText             string `json:"OperationText"`
	WorkCenter                string `json:"WorkCenter,omitempty"`
	Plant                     string `json:"Plant,omitempty"`
	OperationControlKey       string `json:"OperationControlKey,omitempty"`
	OperationStandardDuration string `json:"OperationStandardDuration,omitempty"`
	OperationDurationUnit     string `json:"OperationDurationUnit,omitempty"`
}

// SAP Order Response
type SAPOrderResponse struct {
	D struct {
		MaintenanceOrder           string `json:"MaintenanceOrder"`
		MaintenanceOrderType       string `json:"MaintenanceOrderType"`
		Description                string `json:"Description"`
		Equipment                  string `json:"Equipment"`
		Plant                      string `json:"Plant"`
		OrderStatus                string `json:"OrderStatus"`
		MaintOrdBasicStartDateTime string `json:"MaintOrdBasicStartDateTime"`
		MaintOrdBasicEndDateTime   string `json:"MaintOrdBasicEndDateTime"`
		MaintenanceNotification    string `json:"MaintenanceNotification"`
		Metadata                   struct {
			ID   string `json:"id"`
			URI  string `json:"uri"`
			Type string `json:"type"`
		} `json:"__metadata"`
		ToMaintenanceOrderOperation struct {
			Results []SAPOrderOperationResponse `json:"results"`
		} `json:"to_MaintenanceOrderOperation"`
		ToMaintOrderObjectListItem struct {
			Results []SAPObjectListItemResponse `json:"results"`
		} `json:"to_MaintOrderObjectListItem,omitempty"`
	} `json:"d"`
}

// SAP Order Operation Response
type SAPOrderOperationResponse struct {
	MaintenanceOrder          string `json:"MaintenanceOrder"`
	MaintenanceOrderOperation string `json:"MaintenanceOrderOperation"`
	OperationText             string `json:"OperationText"`
	WorkCenter                string `json:"WorkCenter"`
	OperationControlKey       string `json:"OperationControlKey"`
	OperationStandardDuration string `json:"OperationStandardDuration"`
	OperationDurationUnit     string `json:"OperationDurationUnit"`
	OperationStatus           string `json:"OperationStatus,omitempty"`
	ActualWorkQuantity        string `json:"ActualWorkQuantity,omitempty"`
	WorkQuantityUnit          string `json:"WorkQuantityUnit,omitempty"`
	Metadata                  struct {
		ID   string `json:"id"`
		URI  string `json:"uri"`
		Type string `json:"type"`
	} `json:"__metadata"`
	ToMaintOrderOpComponent2 struct {
		Results []SAPOrderComponentResponse `json:"results"`
	} `json:"to_MaintOrderOpComponent_2,omitempty"`
}

// SAP Order Component Response - Tracks materials/parts consumed
type SAPOrderComponentResponse struct {
	MaintenanceOrder               string `json:"MaintenanceOrder"`
	MaintenanceOrderOperation      string `json:"MaintenanceOrderOperation"`
	MaintenanceOrderSubOperation   string `json:"MaintenanceOrderSubOperation"`
	MaintenanceOrderComponent      string `json:"MaintenanceOrderComponent"`
	Product                        string `json:"Product"`
	MaintOrdOperationComponentText string `json:"MaintOrdOperationComponentText"`
	MaintOrdOpCompRequiredQuantity string `json:"MaintOrdOpCompRequiredQuantity"`
	BaseUnit                       string `json:"BaseUnit"`
	MaintComponentItemCategory     string `json:"MaintComponentItemCategory,omitempty"`
	GoodsMovementType              string `json:"GoodsMovementType,omitempty"`
	Plant                          string `json:"Plant,omitempty"`
	StorageLocation                string `json:"StorageLocation,omitempty"`
	Reservation                    string `json:"Reservation,omitempty"`
	ReservationItem                string `json:"ReservationItem,omitempty"`
	ReservationIsFinallyIssued     bool   `json:"ReservationIsFinallyIssued,omitempty"`
	Metadata                       struct {
		ID   string `json:"id"`
		URI  string `json:"uri"`
		Type string `json:"type"`
	} `json:"__metadata"`
}

// SAP Object List Item Response - Tracks equipment involved
type SAPObjectListItemResponse struct {
	MaintenanceOrder            string `json:"MaintenanceOrder"`
	MaintenanceObjectListItem   int    `json:"MaintenanceObjectListItem"`
	Equipment                   string `json:"Equipment,omitempty"`
	Material                    string `json:"Material,omitempty"`
	SerialNumber                string `json:"SerialNumber,omitempty"`
	Assembly                    string `json:"Assembly,omitempty"`
	FunctionalLocation          string `json:"FunctionalLocation,omitempty"`
	MaintObjectListItemSequence string `json:"MaintObjectListItemSequence,omitempty"`
	Metadata                    struct {
		ID   string `json:"id"`
		URI  string `json:"uri"`
		Type string `json:"type"`
	} `json:"__metadata"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string      `json:"error"`
	Code    string      `json:"code,omitempty"`
	Details interface{} `json:"details,omitempty"`
}

// SuccessResponse represents a success response
type SuccessResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ConvertMaintenanceOrderEventToNotificationRequest converts a MaintenanceOrderEvent to SAP notification request
func ConvertMaintenanceOrderEventToNotificationRequest(event *MaintenanceOrderEvent) *SAPNotificationRequest {
	return &SAPNotificationRequest{
		NotificationType:   "M1", // Default notification type
		Description:        event.Description,
		Equipment:          event.EquipmentID,
		FunctionalLocation: event.FunctionalLocation,
		Plant:              event.Plant,
		Priority:           event.Priority,
	}
}

// ConvertMaintenanceOrderEventToOrderRequest converts a MaintenanceOrderEvent to SAP order request
func ConvertMaintenanceOrderEventToOrderRequest(event *MaintenanceOrderEvent, notificationID string) *SAPOrderRequest {
	req := &SAPOrderRequest{
		MaintenanceOrderType:     event.MaintenanceOrderType,
		Description:              event.Description,
		Equipment:                event.EquipmentID,
		FunctionalLocation:       event.FunctionalLocation,
		Plant:                    event.Plant,
		MaintenancePlanningPlant: event.Plant, // Default to same plant
		Priority:                 event.Priority,
		MaintenanceNotification:  notificationID,
	}

	// Add time fields if provided
	if event.PlannedStartTime != nil {
		req.MaintOrdBasicStartDateTime = event.PlannedStartTime.Format(time.RFC3339)
	}
	if event.PlannedEndTime != nil {
		req.MaintOrdBasicEndDateTime = event.PlannedEndTime.Format(time.RFC3339)
	}

	// Convert operations
	for _, op := range event.Operations {
		sapOp := SAPOrderOperation{
			OperationText:             op.Text,
			WorkCenter:                op.WorkCenter,
			Plant:                     event.Plant,
			OperationControlKey:       event.MaintenanceOrderType,
			OperationStandardDuration: strconv.FormatFloat(op.Duration, 'f', -1, 64),
			OperationDurationUnit:     op.DurationUnit,
		}
		req.ToMaintenanceOrderOperation = append(req.ToMaintenanceOrderOperation, sapOp)
	}

	return req
}
