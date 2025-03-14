from fastapi import FastAPI, File, UploadFile
from PIL import Image
import io
import torch
from yolov5 import detect

app = FastAPI()

# Загрузите предобученную модель YOLOv5
model = torch.hub.load('ultralytics/yolov5', 'yolov5s')

@app.post("/detect/")
async def detect_objects(file: UploadFile = File(...)):
    # Прочитайте изображение из запроса
    image_data = await file.read()
    image = Image.open(io.BytesIO(image_data))

    # Выполните детекцию объектов
    results = model(image)

    # Преобразуйте результаты в JSON
    detections = []
    for detection in results.xyxy[0]:
        x_min, y_min, x_max, y_max, confidence, class_id = detection.tolist()
        detections.append({
            "class_id": int(class_id),
            "class_name": model.names[int(class_id)],
            "confidence": float(confidence),
            "bbox": [float(x_min), float(y_min), float(x_max), float(y_max)]
        })

    return {"detections": detections}