import com.structurizr.view.Dimensions
import com.structurizr.view.Vertex

def positions = [
    'Покупатель': [50, 150],
    'Продавец': [50, 780],
    'Администратор': [50, 1410],
    'API Gateway': [880, 780],
    'Users': [1790, 150],
    'Catalog': [1790, 780],
    'Orders': [1790, 1410],
    'Users DB': [2860, 150],
    'Catalog DB': [2860, 780],
    'Orders DB': [2860, 1410]
]
view.disableAutomaticLayout()
view.mergeFromRemote = false
view.dimensions = new Dimensions(3450, 2180)
view.elements.each { ev ->
    def xy = positions[ev.element.name]
    ev.x = xy[0]
    ev.y = xy[1]
}
view.relationships.each { rv ->
    def source = rv.relationship.source.name
    def target = rv.relationship.destination.name
    rv.vertices = []
    if (source == 'Покупатель') { rv.vertices = [new Vertex(710, 300), new Vertex(710, 800)] }
    else if (source == 'Администратор') { rv.vertices = [new Vertex(710, 1560), new Vertex(710, 1060)] }
    else if (source == 'API Gateway' && target == 'Users') { rv.vertices = [new Vertex(1490, 850), new Vertex(1490, 300)] }
    else if (source == 'API Gateway' && target == 'Orders') { rv.vertices = [new Vertex(1490, 1010), new Vertex(1490, 1560)] }
}
