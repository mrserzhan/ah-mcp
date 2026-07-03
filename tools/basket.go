package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterBasketTools registers shopping-list MCP tools.
func RegisterBasketTools(s *server.MCPServer, deps Deps) {
	registerGetShoppingList(s, deps)
	registerAddToShoppingList(s, deps)
	registerAddFreeTextToShoppingList(s, deps)
	registerRemoveFromShoppingList(s, deps)
	registerCheckShoppingListItem(s, deps)
	registerClearShoppingList(s, deps)
	registerShoppingListToOrder(s, deps)
	registerGetFavoriteLists(s, deps)
	registerAddToFavoriteList(s, deps)
	registerRemoveFromFavoriteList(s, deps)
}

// --- ah_get_shopping_list ---

func registerGetShoppingList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_shopping_list",
		mcp.WithTitleAnnotation("Albert Heijn: View Shopping List"),
		mcp.WithDescription("Get the contents of your Albert Heijn shopping list. Returns item names, quantities, and product IDs."),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		// Read from the v2 shopping list endpoint — the same place AddToShoppingList
		// writes to. The v3 /lists endpoint returns "Mijn Lijstjes" (saved favorite
		// lists), which is a separate feature in the AH app and unrelated to the
		// main Boodschappenlijst.
		type v2Product struct {
			WebshopID int    `json:"webshopId"`
			Title     string `json:"title"`
		}
		type v2ProductDetails struct {
			Product v2Product `json:"product"`
		}
		type v2Item struct {
			ListItemID     int              `json:"listItemId"`
			Quantity       int              `json:"quantity"`
			StrikedThrough bool             `json:"strikedthrough"`
			Position       int              `json:"position"`
			ProductDetails v2ProductDetails `json:"productDetails"`
		}
		type v2Response struct {
			Items []v2Item `json:"items"`
		}
		var raw v2Response
		if err := c.DoRequest(ctx, "GET", "/mobile-services/shoppinglist/v2/items", nil, &raw); err != nil {
			return errResult(fmt.Sprintf("Failed to get shopping list: %v", err)), nil
		}

		if len(raw.Items) == 0 {
			return mcp.NewToolResultText("Your shopping list is empty."), nil
		}

		type entry struct {
			Position  int `json:"position"`
			ItemID    int `json:"item_id,omitempty"`
			Name      string `json:"name"`
			ProductID int    `json:"product_id,omitempty"`
			Quantity  int    `json:"quantity"`
			Checked   bool   `json:"checked,omitempty"`
		}
		entries := make([]entry, 0, len(raw.Items))
		for _, item := range raw.Items {
			name := item.ProductDetails.Product.Title
			pid := item.ProductDetails.Product.WebshopID
			entries = append(entries, entry{
				Position:  item.Position,
				ItemID:    item.ListItemID,
				Name:      name,
				ProductID: pid,
				Quantity:  item.Quantity,
				Checked:   item.StrikedThrough,
			})
		}
		return jsonResult(entries)
	})
}

// --- ah_add_to_shopping_list ---

func registerAddToShoppingList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_add_to_shopping_list",
		mcp.WithTitleAnnotation("Albert Heijn: Add to Shopping List"),
		mcp.WithDescription(
			"Add one or more products to your Albert Heijn shopping list. "+
				"Pass an array of items, each with product_id (int) and quantity (int). "+
				"Returns confirmation listing the names of successfully added products.",
		),
		mcp.WithString("items",
			mcp.Required(),
			mcp.Description(`JSON array of items to add. Each item needs product_id (int) and quantity (int). Example: [{"product_id": 123456, "quantity": 2}]`),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		type inputItem struct {
			ProductID int `json:"product_id"`
			Quantity  int `json:"quantity"`
		}
		var inputItems []inputItem

		// items arrives either as a native []any (some clients) or as a
		// JSON-encoded string "[{...}]" (Claude Desktop / Claude Code).
		rawItems := req.GetArguments()["items"]
		switch v := rawItems.(type) {
		case string:
			if err := json.Unmarshal([]byte(v), &inputItems); err != nil {
				return errResult(fmt.Sprintf("items must be a JSON array: %v", err)), nil
			}
		case []any:
			for _, raw := range v {
				m, mOK := raw.(map[string]any)
				if !mOK {
					continue
				}
				pid := toInt(m["product_id"])
				qty := toInt(m["quantity"])
				if pid > 0 && qty > 0 {
					inputItems = append(inputItems, inputItem{ProductID: pid, Quantity: qty})
				}
			}
		case nil:
			return errResult("items parameter is required"), nil
		default:
			return errResult("items must be a JSON array"), nil
		}

		if len(inputItems) == 0 {
			return errResult("no valid items provided (each item needs product_id > 0 and quantity > 0)"), nil
		}

		listItems := make([]appie.ListItem, 0, len(inputItems))
		for _, it := range inputItems {
			listItems = append(listItems, appie.ListItem{
				ProductID: it.ProductID,
				Quantity:  it.Quantity,
			})
		}

		if err := c.AddToShoppingList(ctx, listItems); err != nil {
			return errResult(fmt.Sprintf("Failed to add items: %v", err)), nil
		}

		// Fetch product names for the confirmation message.
		var pids []int
		for _, it := range inputItems {
			pids = append(pids, it.ProductID)
		}
		products, pErr := c.GetProductsByIDs(ctx, pids)
		nameMap := map[int]string{}
		if pErr == nil {
			for _, p := range products {
				nameMap[p.ID] = p.Title
			}
		}

		names := make([]string, 0, len(inputItems))
		for _, it := range inputItems {
			if n, ok := nameMap[it.ProductID]; ok {
				names = append(names, fmt.Sprintf("%s (x%d)", n, it.Quantity))
			} else {
				names = append(names, fmt.Sprintf("Product %d (x%d)", it.ProductID, it.Quantity))
			}
		}
		return mcp.NewToolResultText(fmt.Sprintf("Added to shopping list:\n- %s", strings.Join(names, "\n- "))), nil
	})
}

// toInt converts interface{} to int, supporting float64 (JSON numbers) and int.
func toInt(v any) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	default:
		return 0
	}
}

// --- ah_add_free_text_to_shopping_list ---

func registerAddFreeTextToShoppingList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_add_free_text_to_shopping_list",
		mcp.WithTitleAnnotation("Albert Heijn: Add Free-Text to Shopping List"),
		mcp.WithDescription(
			"Add a free-text item to the Albert Heijn shopping list (no product ID needed). "+
				"Use for reminders like 'verse bloemen', 'any good wine', or items not found in search.",
		),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("Free-text item description, e.g. 'verse bloemen', 'goede rode wijn'"),
		),
		mcp.WithString("quantity",
			mcp.Description("Quantity (default 1)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		name := req.GetString("name", "")
		if name == "" {
			return errResult("name is required"), nil
		}
		quantity := req.GetInt("quantity", 1)
		if quantity < 1 {
			quantity = 1
		}

		if err := c.AddFreeTextToShoppingList(ctx, name, quantity); err != nil {
			return errResult(fmt.Sprintf("Failed to add free-text item: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Added '%s' (x%d) to shopping list.", name, quantity)), nil
	})
}

// --- ah_remove_from_shopping_list ---

func registerRemoveFromShoppingList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_remove_from_shopping_list",
		mcp.WithTitleAnnotation("Albert Heijn: Remove from Shopping List"),
		mcp.WithDescription(
			"Remove one or more items from the Albert Heijn Boodschappenlijst. "+
				"For product items pass product_ids; for free-text items pass names. "+
				"Get product_ids from ah_get_shopping_list.",
		),
		mcp.WithString("product_ids",
			mcp.Description("JSON array of product IDs to remove, e.g. [123456, 789012]. Use for product items."),
		),
		mcp.WithString("names",
			mcp.Description(`JSON array of free-text item names to remove, e.g. ["verse bloemen"]. Use for items without a product ID.`),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		// Parse product_ids
		var productIDs []int
		if raw := req.GetArguments()["product_ids"]; raw != nil {
			switch v := raw.(type) {
			case string:
				if err := json.Unmarshal([]byte(v), &productIDs); err != nil {
					return errResult(fmt.Sprintf("product_ids must be a JSON array of integers: %v", err)), nil
				}
			case []any:
				for _, r := range v {
					if pid := toInt(r); pid > 0 {
						productIDs = append(productIDs, pid)
					}
				}
			}
		}

		// Parse names (for free-text items)
		var names []string
		if raw := req.GetArguments()["names"]; raw != nil {
			switch v := raw.(type) {
			case string:
				if err := json.Unmarshal([]byte(v), &names); err != nil {
					return errResult(fmt.Sprintf("names must be a JSON array of strings: %v", err)), nil
				}
			case []any:
				for _, r := range v {
					if s, ok := r.(string); ok && s != "" {
						names = append(names, s)
					}
				}
			}
		}

		if len(productIDs) == 0 && len(names) == 0 {
			return errResult("provide at least one product_id or name to remove"), nil
		}

		// First fetch the current list so we can identify item positions.
		type v2RawItem struct {
			ListItemID int    `json:"listItemId"`
			Position   int    `json:"position"`
			OriginCode string `json:"originCode"`
			Quantity   int    `json:"quantity"`
			Type       string `json:"type"`
			Description string `json:"description"`
			ProductDetails struct {
				Product struct {
					WebshopID int    `json:"webshopId"`
					Title     string `json:"title"`
				} `json:"product"`
			} `json:"productDetails"`
		}
		type v2ListResp struct {
			Items []v2RawItem `json:"items"`
		}
		var currentList v2ListResp
		if err := c.DoRequest(ctx, "GET", "/mobile-services/shoppinglist/v2/items", nil, &currentList); err != nil {
			return errResult(fmt.Sprintf("Failed to read shopping list: %v", err)), nil
		}

		// Build set of things to remove.
		removeByProductID := map[int]bool{}
		for _, pid := range productIDs {
			removeByProductID[pid] = true
		}
		removeByName := map[string]bool{}
		for _, n := range names {
			removeByName[strings.ToLower(n)] = true
		}

		// Filter — keep items NOT in the remove sets.
		type keepItem struct {
			ProductID   int    `json:"productId,omitempty"`
			Description string `json:"description,omitempty"`
			Quantity    int    `json:"quantity"`
			Type        string `json:"type"`
			OriginCode  string `json:"originCode"`
		}
		var keepItems []keepItem
		for _, it := range currentList.Items {
			pid := it.ProductDetails.Product.WebshopID
			desc := strings.ToLower(it.Description)
			if removeByProductID[pid] || removeByName[desc] {
				continue // drop this item
			}
			keepItems = append(keepItems, keepItem{
				ProductID:   pid,
				Description: it.Description,
				Quantity:    it.Quantity,
				Type:        it.Type,
				OriginCode:  it.OriginCode,
			})
		}

		// Build the removal PATCH payload — quantity 0 signals deletion on the v2 API.
		type removeItem struct {
			ProductID     int    `json:"productId,omitempty"`
			Description   string `json:"description,omitempty"`
			Quantity      int    `json:"quantity"`
			Type          string `json:"type"`
			OriginCode    string `json:"originCode"`
			StrikeThrough bool   `json:"strikeThrough"`
		}
		var removeItems []removeItem
		for _, it := range currentList.Items {
			pid := it.ProductDetails.Product.WebshopID
			desc := strings.ToLower(it.Description)
			if removeByProductID[pid] || removeByName[desc] {
				// The v2 PATCH endpoint rejects items with empty type/originCode
				// ("400 Failed to read request") — the GET response does not echo
				// these fields, so default them to the values AddToShoppingList
				// writes (type SHOPPABLE, originCode PRD) and fall back to the
				// product title when description is empty.
				oc := it.OriginCode
				if oc == "" {
					oc = "PRD"
				}
				ty := it.Type
				if ty == "" {
					ty = "SHOPPABLE"
				}
				d := it.Description
				if d == "" {
					d = it.ProductDetails.Product.Title
				}
				removeItems = append(removeItems, removeItem{
					ProductID:     pid,
					Description:   d,
					Quantity:      0,
					Type:          ty,
					OriginCode:    oc,
					StrikeThrough: false,
				})
			}
		}
		if len(removeItems) == 0 {
			return errResult("no matching items found on the shopping list"), nil
		}
		patchBody := map[string]any{"items": removeItems}
		if err := c.DoRequest(ctx, "PATCH", "/mobile-services/shoppinglist/v2/items", patchBody, nil); err != nil {
			return errResult(fmt.Sprintf("Failed to update shopping list: %v", err)), nil
		}

		var removed []string
		for _, pid := range productIDs {
			removed = append(removed, fmt.Sprintf("product %d", pid))
		}
		removed = append(removed, names...)
		return mcp.NewToolResultText(fmt.Sprintf("Removed from shopping list: %s", strings.Join(removed, ", "))), nil
	})
}

// --- ah_check_shopping_list_item ---

func registerCheckShoppingListItem(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_check_shopping_list_item",
		mcp.WithTitleAnnotation("Albert Heijn: Check Shopping List Item"),
		mcp.WithDescription(
			"Mark a shopping list item as checked (picked up) or uncheck it. "+
				"NOTE: The AH v2 Boodschappenlijst does not expose per-item IDs (listItemId is always 0), "+
				"so this tool only works for items in 'Mijn Lijstjes' (favorite lists) that have real string item IDs. "+
				"Checking items in the main shopping list must be done in the AH app directly.",
		),
		mcp.WithString("item_id",
			mcp.Required(),
			mcp.Description("Item ID (string) from a favorite list — NOT from ah_get_shopping_list (those have no usable IDs)"),
		),
		mcp.WithString("checked",
			mcp.Description("'true' to check the item (default), 'false' to uncheck"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		itemID := req.GetString("item_id", "")
		if itemID == "" {
			return errResult("item_id is required"), nil
		}
		checked := req.GetString("checked", "true") != "false"

		if err := c.CheckShoppingListItem(ctx, itemID, checked); err != nil {
			return errResult(fmt.Sprintf("Failed to update item %s: %v", itemID, err)), nil
		}
		state := "checked"
		if !checked {
			state = "unchecked"
		}
		return mcp.NewToolResultText(fmt.Sprintf("Item %s marked as %s.", itemID, state)), nil
	})
}

// --- ah_clear_shopping_list ---

func registerClearShoppingList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_clear_shopping_list",
		mcp.WithTitleAnnotation("Albert Heijn: Clear Shopping List"),
		mcp.WithDescription(
			"Remove ALL items from the Albert Heijn shopping list. "+
				"Irreversible — requires confirm=\"yes\".",
		),
		mcp.WithString("confirm",
			mcp.Required(),
			mcp.Description("Must be \"yes\" to confirm clearing the list"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if req.GetString("confirm", "") != "yes" {
			return errResult("confirm must be \"yes\" to clear the shopping list"), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		// Read all v2 items, then PATCH each with quantity=0 to remove them.
		type v2ClearItem struct {
			ListItemID int    `json:"listItemId"`
			OriginCode string `json:"originCode"`
			Quantity   int    `json:"quantity"`
			Type       string `json:"type"`
			Description string `json:"description"`
			ProductDetails struct {
				Product struct {
					WebshopID int    `json:"webshopId"`
					Title     string `json:"title"`
				} `json:"product"`
			} `json:"productDetails"`
		}
		type v2ClearResp struct {
			Items []v2ClearItem `json:"items"`
		}
		var current v2ClearResp
		if err := c.DoRequest(ctx, "GET", "/mobile-services/shoppinglist/v2/items", nil, &current); err != nil {
			return errResult(fmt.Sprintf("Failed to read shopping list: %v", err)), nil
		}
		if len(current.Items) == 0 {
			return mcp.NewToolResultText("Shopping list is already empty."), nil
		}
		type zeroItem struct {
			ProductID     int    `json:"productId,omitempty"`
			Description   string `json:"description,omitempty"`
			Quantity      int    `json:"quantity"`
			Type          string `json:"type"`
			OriginCode    string `json:"originCode"`
			StrikeThrough bool   `json:"strikeThrough"`
		}
		zeros := make([]zeroItem, 0, len(current.Items))
		for _, it := range current.Items {
			// Same empty-field defaults as ah_remove_from_shopping_list — the
			// v2 PATCH endpoint 400s on empty type/originCode.
			oc := it.OriginCode
			if oc == "" {
				oc = "PRD"
			}
			ty := it.Type
			if ty == "" {
				ty = "SHOPPABLE"
			}
			d := it.Description
			if d == "" {
				d = it.ProductDetails.Product.Title
			}
			zeros = append(zeros, zeroItem{
				ProductID:     it.ProductDetails.Product.WebshopID,
				Description:   d,
				Quantity:      0,
				Type:          ty,
				OriginCode:    oc,
				StrikeThrough: false,
			})
		}
		if err := c.DoRequest(ctx, "PATCH", "/mobile-services/shoppinglist/v2/items", map[string]any{"items": zeros}, nil); err != nil {
			return errResult(fmt.Sprintf("Failed to clear shopping list: %v", err)), nil
		}
		return mcp.NewToolResultText("Shopping list cleared."), nil
	})
}

// --- ah_shopping_list_to_order ---

func registerShoppingListToOrder(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_shopping_list_to_order",
		mcp.WithTitleAnnotation("Albert Heijn: Move List to Cart"),
		mcp.WithDescription(
			"Add all unchecked product items from the Albert Heijn shopping list to the online order (cart). "+
				"Free-text items and already-checked items are skipped. "+
				"Use ah_get_cart to review the result.",
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		if err := c.ShoppingListToOrder(ctx); err != nil {
			return errResult(fmt.Sprintf("Failed to move shopping list to order: %v", err)), nil
		}
		return mcp.NewToolResultText("Shopping list items added to your online order. Use ah_get_cart to review."), nil
	})
}

// --- ah_get_favorite_lists ---

func registerGetFavoriteLists(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_get_favorite_lists",
		mcp.WithTitleAnnotation("Albert Heijn: View Favourite Lists"),
		mcp.WithDescription(
			"List all Albert Heijn favorite/saved shopping lists with their names and item counts. "+
				"Use the returned list ID with ah_add_to_favorite_list or ah_remove_from_favorite_list.",
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		lists, err := c.GetShoppingLists(ctx, 0)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get favorite lists: %v", err)), nil
		}

		type entry struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			ItemCount int    `json:"item_count"`
		}
		results := make([]entry, 0, len(lists))
		for _, l := range lists {
			results = append(results, entry{ID: l.ID, Name: l.Name, ItemCount: l.ItemCount})
		}
		return jsonResult(results)
	})
}

// --- ah_add_to_favorite_list ---

func registerAddToFavoriteList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_add_to_favorite_list",
		mcp.WithTitleAnnotation("Albert Heijn: Add to Favourite List"),
		mcp.WithDescription(
			"Add products to a named Albert Heijn favorite list. "+
				"Get list_id from ah_get_favorite_lists.",
		),
		mcp.WithString("list_id",
			mcp.Required(),
			mcp.Description("Favorite list ID from ah_get_favorite_lists"),
		),
		mcp.WithString("items",
			mcp.Required(),
			mcp.Description("JSON array of items: [{\"product_id\": 123456, \"quantity\": 1}]"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		listID := req.GetString("list_id", "")
		if listID == "" {
			return errResult("list_id is required"), nil
		}

		type inputItem struct {
			ProductID int `json:"product_id"`
			Quantity  int `json:"quantity"`
		}
		var inputItems []inputItem
		rawItems := req.GetArguments()["items"]
		switch v := rawItems.(type) {
		case string:
			if err := json.Unmarshal([]byte(v), &inputItems); err != nil {
				return errResult(fmt.Sprintf("items must be a JSON array: %v", err)), nil
			}
		case []any:
			for _, raw := range v {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				pid := toInt(m["product_id"])
				qty := toInt(m["quantity"])
				if qty == 0 {
					qty = 1
				}
				if pid > 0 {
					inputItems = append(inputItems, inputItem{ProductID: pid, Quantity: qty})
				}
			}
		default:
			return errResult("items must be a JSON array"), nil
		}
		if len(inputItems) == 0 {
			return errResult("no valid items provided"), nil
		}

		listItems := make([]appie.ListItem, 0, len(inputItems))
		for _, it := range inputItems {
			listItems = append(listItems, appie.ListItem{ProductID: it.ProductID, Quantity: it.Quantity})
		}
		if err := c.AddToFavoriteList(ctx, listID, listItems); err != nil {
			return errResult(fmt.Sprintf("Failed to add to favorite list: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Added %d item(s) to favorite list %s.", len(listItems), listID)), nil
	})
}

// --- ah_remove_from_favorite_list ---

func registerRemoveFromFavoriteList(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_remove_from_favorite_list",
		mcp.WithTitleAnnotation("Albert Heijn: Remove from Favourite List"),
		mcp.WithDescription(
			"Remove products from a named Albert Heijn favorite list. "+
				"Get list_id from ah_get_favorite_lists.",
		),
		mcp.WithString("list_id",
			mcp.Required(),
			mcp.Description("Favorite list ID from ah_get_favorite_lists"),
		),
		mcp.WithString("product_ids",
			mcp.Required(),
			mcp.Description("JSON array of product IDs to remove, e.g. [123456, 789012]"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		listID := req.GetString("list_id", "")
		if listID == "" {
			return errResult("list_id is required"), nil
		}

		var productIDs []int
		rawIDs := req.GetArguments()["product_ids"]
		switch v := rawIDs.(type) {
		case string:
			if err := json.Unmarshal([]byte(v), &productIDs); err != nil {
				return errResult(fmt.Sprintf("product_ids must be a JSON array of integers: %v", err)), nil
			}
		case []any:
			for _, raw := range v {
				pid := toInt(raw)
				if pid > 0 {
					productIDs = append(productIDs, pid)
				}
			}
		default:
			return errResult("product_ids must be a JSON array of integers"), nil
		}
		if len(productIDs) == 0 {
			return errResult("no valid product_ids provided"), nil
		}

		if err := c.RemoveFromFavoriteList(ctx, listID, productIDs); err != nil {
			return errResult(fmt.Sprintf("Failed to remove from favorite list: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Removed %d product(s) from favorite list %s.", len(productIDs), listID)), nil
	})
}
